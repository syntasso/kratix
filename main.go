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

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"go.uber.org/zap/zapcore"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/yaml"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	corev1 "k8s.io/api/core/v1"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/lib/fetchers"
	//+kubebuilder:scaffold:imports
)

var setupLog = ctrl.Log.WithName("setup")

func init() {
	utilruntime.Must(platformv1alpha1.AddToScheme(scheme.Scheme))
	//+kubebuilder:scaffold:scheme
}

type KratixConfig struct {
	Workflows          Workflows `json:"workflows"`
	NumberOfJobsToKeep int       `json:"numberOfJobsToKeep,omitempty"`
}

type Workflows struct {
	DefaultContainerSecurityContext corev1.SecurityContext `json:"defaultContainerSecurityContext"`
}

func main() {
	var metricsAddr string
	var probeAddr string
	var pprofAddr string
	var enableLeaderElection bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&pprofAddr, "pprof-bind-address", ":8082", "The address the pprof endpoint binds to.")
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
	}

	for {
		config := ctrl.GetConfigOrDie()
		apiextensionsClient := clientset.NewForConfigOrDie(config)
		metricsServerOptions := metricsserver.Options{
			BindAddress: metricsAddr,
		}
		webhookServer := webhook.NewServer(webhook.Options{
			Port: 9443,
		})

		mgr, err := ctrl.NewManager(config, ctrl.Options{
			Scheme:                 scheme.Scheme,
			Metrics:                metricsServerOptions,
			WebhookServer:          webhookServer,
			HealthProbeBindAddress: probeAddr,
			PprofBindAddress:       pprofAddr,
			LeaderElection:         enableLeaderElection,
			LeaderElectionID:       "2743c979.kratix.io",
		})
		if err != nil {
			setupLog.Error(err, "unable to start manager")
			os.Exit(1)
		}

		scheduler := controllers.Scheduler{
			Client: mgr.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
		}

		restartManager := false
		restartManagerInProgress := false
		if err = (&controllers.PromiseReconciler{
			ApiextensionsClient: apiextensionsClient.ApiextensionsV1(),
			Client:              mgr.GetClient(),
			Log:                 ctrl.Log.WithName("controllers").WithName("Promise"),
			Manager:             mgr,
			Scheme:              mgr.GetScheme(),
			NumberOfJobsToKeep:  getNumJobsToKeep(kratixConfig),
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
		if err = (&controllers.WorkReconciler{
			Client:    mgr.GetClient(),
			Log:       ctrl.Log.WithName("controllers").WithName("Work"),
			Scheduler: &scheduler,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Work")
			os.Exit(1)
		}
		if err = (&controllers.DestinationReconciler{
			Client:    mgr.GetClient(),
			Scheduler: &scheduler,
			Log:       ctrl.Log.WithName("controllers").WithName("DestinationController"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Destination")
			os.Exit(1)
		}
		if err = (&controllers.WorkPlacementReconciler{
			Client:       mgr.GetClient(),
			Log:          ctrl.Log.WithName("controllers").WithName("WorkPlacementController"),
			VersionCache: make(map[string]string),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "WorkPlacement")
			os.Exit(1)
		}
		if err = (&platformv1alpha1.Promise{}).SetupWebhookWithManager(mgr, apiextensionsClient, mgr.GetClient()); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Promise")
			os.Exit(1)
		}
		if err = (&controllers.PromiseReleaseReconciler{
			Log:            ctrl.Log.WithName("controllers").WithName("PromiseReleaseController"),
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			PromiseFetcher: &fetchers.URLFetcher{},
			EventRecorder:  mgr.GetEventRecorderFor("PromiseReleaseController"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "PromiseRelease")
			os.Exit(1)
		}
		if err = (&platformv1alpha1.PromiseRelease{}).SetupWebhookWithManager(mgr, mgr.GetClient(), &fetchers.URLFetcher{}); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "PromiseRelease")
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
