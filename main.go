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
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"go.uber.org/zap/zapcore"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

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

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctx, cancelManagerCtxFunc := context.WithCancel(context.Background())
	restartManager := false
	for {
		ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts), func(o *zap.Options) {
			o.TimeEncoder = zapcore.TimeEncoderOfLayout("2006-01-02T15:04:05Z07:00")
		}))

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

		if err = (&controllers.PromiseReconciler{
			ApiextensionsClient: apiextensionsClient,
			Client:              mgr.GetClient(),
			Log:                 ctrl.Log.WithName("controllers").WithName("Promise"),
			Manager:             mgr,
			RestartManager: func() {
				restartManager = true
				cancelManagerCtxFunc()
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
			Client: mgr.GetClient(),
			Log:    ctrl.Log.WithName("controllers").WithName("WorkPlacementController"),
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
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "PromiseRelease")
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
