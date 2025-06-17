/*
Copyright 2021 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/syntasso/kratix/internal/ptr"
	"go.uber.org/zap/zapcore"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/yaml"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/internal/controller"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	controllercfg "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kratixWebhook "github.com/syntasso/kratix/internal/webhook/v1alpha1"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"

	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/fetchers"
	//+kubebuilder:scaffold:imports
)

var setupLog = ctrl.Log.WithName("setup")

func init() {
	utilruntime.Must(platformv1alpha1.AddToScheme(scheme.Scheme))
	//+kubebuilder:scaffold:scheme
}

type KratixConfig struct {
	Workflows                Workflows             `json:"workflows"`
	NumberOfJobsToKeep       int                   `json:"numberOfJobsToKeep,omitempty"`
	ControllerLeaderElection *LeaderElectionConfig `json:"controllerLeaderElection,omitempty"`
	SelectiveCache           bool                  `json:"selectiveCache,omitempty"`
	ReconciliationInterval   *metav1.Duration      `json:"reconciliationInterval,omitempty"`
}

type Workflows struct {
	DefaultContainerSecurityContext corev1.SecurityContext `json:"defaultContainerSecurityContext"`
	DefaultImagePullPolicy          corev1.PullPolicy      `json:"defaultImagePullPolicy,omitempty"`
	JobOptions                      JobOptions             `json:"jobOptions,omitempty"`
}

type JobOptions struct {
	DefaultBackoffLimit *int32 `json:"defaultBackoffLimit,omitempty"`
}

// LeaderElectionConfig duration default can be found in:
// https://github.com/kubernetes-sigs/controller-runtime/blob/561fa39c550f458eb6fb81bf70b9c02a190ec7bc/pkg/manager/manager.go#L210-L221
type LeaderElectionConfig struct {
	LeaseDuration *metav1.Duration `json:"leaseDuration,omitempty"`
	RenewDeadline *metav1.Duration `json:"renewDeadline,omitempty"`
	RetryPeriod   *metav1.Duration `json:"retryPeriod,omitempty"`
}

var metricsAddr string
var probeAddr string
var secureMetrics bool
var pprofAddr string
var enableLeaderElection bool

//nolint:funlen
func main() {
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&pprofAddr, "pprof-bind-address", ":8082", "The address the pprof endpoint binds to.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctx, cancelManagerCtxFunc := context.WithCancel(context.Background())
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts), func(o *zap.Options) {
		o.TimeEncoder = zapcore.TimeEncoderOfLayout("2006-01-02T15:04:05Z07:00")
	}))

	prefix := os.Getenv("KRATIX_LOGGER_PREFIX")
	if prefix != "" {
		ctrl.Log = ctrl.Log.WithName(prefix)
	}

	kClient, err := client.New(ctrl.GetConfigOrDie(), client.Options{})
	if err != nil {
		panic(err)
	}

	kratixConfig, err := readKratixConfig(ctrl.Log, kClient)
	if err != nil {
		panic(err)
	}

	if kratixConfig != nil {
		v1alpha1.DefaultUserProvidedContainersSecurityContext = &kratixConfig.Workflows.DefaultContainerSecurityContext
		v1alpha1.DefaultImagePullPolicy = kratixConfig.Workflows.DefaultImagePullPolicy
		if kratixConfig.Workflows.JobOptions.DefaultBackoffLimit != nil {
			v1alpha1.DefaultJobBackoffLimit = kratixConfig.Workflows.JobOptions.DefaultBackoffLimit
		}
	}

	for {
		config := ctrl.GetConfigOrDie()
		apiextensionsClient := clientset.NewForConfigOrDie(config)
		metricsServerOptions := metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
		}
		webhookServer := webhook.NewServer(webhook.Options{
			Port: 9443,
		})

		if secureMetrics {
			// FilterProvider is used to protect the metrics endpoint with authn/authz.
			// These configurations ensure that only authorized users and service accounts
			// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
			// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/filters#WithAuthenticationAndAuthorization
			metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization

			// TODO(user): If CertDir, CertName, and KeyName are not specified, controller-runtime will automatically
			// generate self-signed certificates for the metrics server. While convenient for development and testing,
			// this setup is not recommended for production.
		}

		mgrOptions := ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsServerOptions,
			WebhookServer:          webhookServer,
			HealthProbeBindAddress: probeAddr,
			PprofBindAddress:       pprofAddr,
			LeaderElection:         enableLeaderElection,
			LeaderElectionID:       "2743c979.kratix.io",
			Controller: controllercfg.Controller{
				SkipNameValidation: ptr.True(),
			},
		}

		if kratixConfig != nil && kratixConfig.ControllerLeaderElection != nil {
			setLeaderElectConfig(&mgrOptions, kratixConfig)
		}

		if kratixConfig != nil && kratixConfig.SelectiveCache {
			setupLog.Info("Building selective cache for Secrets to limit memory usage; Please ensure Secrets used by kratix are created with label: app.kubernetes.io/part-of=kratix.")
			kratixLabel, labelErr := labels.NewRequirement("app.kubernetes.io/part-of", selection.Equals, []string{"kratix"})
			if labelErr != nil {
				setupLog.Error(labelErr, "unable to create a label filter")
				os.Exit(1)
			}
			kratixSelector := labels.NewSelector().Add(*kratixLabel)
			mgrOptions.Cache.ByObject = map[client.Object]cache.ByObject{
				&corev1.Secret{}: {Label: kratixSelector},
			}
		}

		mgr, err := ctrl.NewManager(config, mgrOptions)
		if err != nil {
			setupLog.Error(err, "unable to start manager")
			os.Exit(1)
		}

		scheduler := controller.Scheduler{
			Client:        mgr.GetClient(),
			Log:           ctrl.Log.WithName("controllers").WithName("Scheduler"),
			EventRecorder: mgr.GetEventRecorderFor("Scheduler"),
		}

		restartManager := false
		restartManagerInProgress := false
		if err = (&controller.PromiseReconciler{
			ApiextensionsClient:    apiextensionsClient.ApiextensionsV1(),
			Client:                 mgr.GetClient(),
			Log:                    ctrl.Log.WithName("controllers").WithName("Promise"),
			Manager:                mgr,
			Scheme:                 mgr.GetScheme(),
			NumberOfJobsToKeep:     getNumJobsToKeep(kratixConfig),
			ReconciliationInterval: getRegularReconciliationInterval(kratixConfig),
			EventRecorder:          mgr.GetEventRecorderFor("PromiseController"),
			RestartManager: func() {
				// This function gets called multiple times
				// First call: restartInProgress get set to true, sleeps starts
				// Following calls: no-op
				// Once sleep finishes: restartInProgress set to false.
				restartManager = true
				if !restartManagerInProgress {
					// start in a go routine to avoid blocking the main thread
					go func() {
						restartManagerInProgress = true
						time.Sleep(time.Minute * 2)
						restartManagerInProgress = false
						cancelManagerCtxFunc()
					}()
				}
			},
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Promise")
			os.Exit(1)
		}
		if err = (&controller.WorkReconciler{
			Client:        mgr.GetClient(),
			Log:           ctrl.Log.WithName("controllers").WithName("Work"),
			Scheduler:     &scheduler,
			EventRecorder: mgr.GetEventRecorderFor("WorkController"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Work")
			os.Exit(1)
		}
		if err = (&controller.DestinationReconciler{
			Client:        mgr.GetClient(),
			Scheduler:     &scheduler,
			Log:           ctrl.Log.WithName("controllers").WithName("DestinationController"),
			EventRecorder: mgr.GetEventRecorderFor("DestinationController"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Destination")
			os.Exit(1)
		}
		if err = (&controller.WorkPlacementReconciler{
			Client:        mgr.GetClient(),
			Log:           ctrl.Log.WithName("controllers").WithName("WorkPlacementController"),
			VersionCache:  make(map[string]string),
			EventRecorder: mgr.GetEventRecorderFor("WorkPlacementController"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "WorkPlacement")
			os.Exit(1)
		}
		if err = kratixWebhook.SetupPromiseWebhookWithManager(mgr, apiextensionsClient, mgr.GetClient()); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Promise")
			os.Exit(1)
		}
		if err = (&controller.PromiseReleaseReconciler{
			Log:            ctrl.Log.WithName("controllers").WithName("PromiseReleaseController"),
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			PromiseFetcher: &fetchers.URLFetcher{},
			EventRecorder:  mgr.GetEventRecorderFor("PromiseReleaseController"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "PromiseRelease")
			os.Exit(1)
		}
		if err = kratixWebhook.SetupPromiseReleaseWebhookWithManager(mgr, mgr.GetClient(), &fetchers.URLFetcher{}); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "PromiseRelease")
			os.Exit(1)
		}
		if err = (&controller.HealthRecordReconciler{
			Client:        mgr.GetClient(),
			Scheme:        mgr.GetScheme(),
			Log:           ctrl.Log.WithName("controllers").WithName("HealthRecordController"),
			EventRecorder: mgr.GetEventRecorderFor("HealthRecordController"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "HealthRecord")
			os.Exit(1)
		}
		if err = kratixWebhook.SetupDestinationWebhookWithManager(mgr, mgr.GetClient()); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Destination")
			os.Exit(1)
		}
		if err = (&controller.BucketStateStoreReconciler{
			Client:        mgr.GetClient(),
			Scheme:        mgr.GetScheme(),
			Log:           ctrl.Log.WithName("controllers").WithName("BucketStateStoreController"),
			EventRecorder: mgr.GetEventRecorderFor("BucketStateStoreController"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "BucketStateStore")
			os.Exit(1)
		}
		if err = (&controller.GitStateStoreReconciler{
			Client:        mgr.GetClient(),
			Scheme:        mgr.GetScheme(),
			Log:           ctrl.Log.WithName("controllers").WithName("GitStateStoreController"),
			EventRecorder: mgr.GetEventRecorderFor("GitStateStoreController"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "GitStateStore")
			os.Exit(1)
		}
		if err = kratixWebhook.SetupBucketStateStoreWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "BucketStateStore")
			os.Exit(1)
		}
		//+kubebuilder:scaffold:builder

		if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
			setupLog.Error(err, "unable to set up health check")
			os.Exit(1)
		}
		if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
			setupLog.Error(err, "unable to set up ready check")
			os.Exit(1)
		}

		setupLog.Info("starting manager")
		err = mgr.Start(ctx)
		setupLog.Info("manager stopped")

		if !restartManager {
			if err != nil {
				setupLog.Error(err, "problem running manager")
				os.Exit(1)
			}
			setupLog.Info("shutting down")
			os.Exit(0)
		}

		setupLog.Info("restarting manager")
		ctx, cancelManagerCtxFunc = context.WithCancel(context.Background())
		restartManager = false
	}
}

const numJobsToKeepDefault = 5

func readKratixConfig(logger logr.Logger, kClient client.Client) (*KratixConfig, error) {
	cm := &corev1.ConfigMap{}
	err := kClient.Get(context.Background(), client.ObjectKey{Namespace: "kratix-platform-system", Name: "kratix"}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("kratix-platform-system/kratix ConfigMap not found, using default config")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get kratix-platform-system/kratix configmap: %w", err)
	}

	logger.Info("kratix-platform-system/kratix ConfigMap found")
	config, exists := cm.Data["config"]
	if !exists {
		return nil, fmt.Errorf("configmap kratix-platform-system/kratix does not contain a 'config' key")
	}

	kratixConfig := &KratixConfig{}
	err = yaml.Unmarshal([]byte(config), kratixConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal ConfigMap kratix-platform-system/kratix into Kratix config: %w", err)
	}

	logger.Info("Kratix config loaded", "config", kratixConfig)

	return kratixConfig, nil
}

func getNumJobsToKeep(kratixConfig *KratixConfig) int {
	if kratixConfig == nil || kratixConfig.NumberOfJobsToKeep == 0 {
		return numJobsToKeepDefault
	}
	if kratixConfig.NumberOfJobsToKeep < 1 {
		setupLog.Error(fmt.Errorf("invalid Kratix Config"),
			"numberOfJobsToKeep cannot be less than one; set to default value",
			"numberOfJobsToKeep", kratixConfig.NumberOfJobsToKeep)
		return numJobsToKeepDefault
	}
	return kratixConfig.NumberOfJobsToKeep
}

func getRegularReconciliationInterval(kratixConfig *KratixConfig) time.Duration {
	if kratixConfig == nil || kratixConfig.ReconciliationInterval == nil {
		setupLog.Info("reconciliationInterval is nil; setting to the default value",
			"defaultReconciliationInterval", controller.DefaultReconciliationInterval)
		return controller.DefaultReconciliationInterval
	}
	return kratixConfig.ReconciliationInterval.Duration
}

func setLeaderElectConfig(mgrOptions *ctrl.Options, kConfig *KratixConfig) {
	if kConfig.ControllerLeaderElection.LeaseDuration != nil {
		mgrOptions.LeaseDuration = &kConfig.ControllerLeaderElection.LeaseDuration.Duration
		setupLog.Info("controller leader election configured", "LeaseDuration", mgrOptions.LeaseDuration)
	}
	if kConfig.ControllerLeaderElection.RenewDeadline != nil {
		mgrOptions.RenewDeadline = &kConfig.ControllerLeaderElection.RenewDeadline.Duration
		setupLog.Info("controller leader election configured", "RenewDeadline", mgrOptions.RenewDeadline)
	}
	if kConfig.ControllerLeaderElection.RetryPeriod != nil {
		mgrOptions.RetryPeriod = &kConfig.ControllerLeaderElection.RetryPeriod.Duration
		setupLog.Info("controller leader election configured", "RetryPeriod", mgrOptions.RetryPeriod)
	}
}
