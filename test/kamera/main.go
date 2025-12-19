package main

import (
	"context"
	"flag"
	"fmt"

	kratix "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/tgoodwin/kamera/pkg/explore"
	"github.com/tgoodwin/kamera/pkg/tracecheck"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type noopScheduler struct{}

func (noopScheduler) ReconcileWork(context.Context, *kratix.Work) ([]string, error) {
	return nil, nil
}

func main() {
	flag.Parse()

	scheme := runtime.NewScheme()
	utilruntime.Must(kratix.AddToScheme(scheme))

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	tracecheck.SetLogger(ctrl.Log.WithName("tracecheck"))

	const workControllerID = "WorkController"
	workKind := "platform.kratix.io/Work"

	eb := tracecheck.NewExplorerBuilder(scheme)
	eb.WithMaxDepth(6)
	eb.WithReconciler(workControllerID, func(c ctrlclient.Client) tracecheck.Reconciler {
		return &controller.WorkReconciler{
			Client:        c,
			Log:           ctrl.Log.WithName("work"),
			Scheduler:     noopScheduler{},
			EventRecorder: record.NewFakeRecorder(32),
		}
	}).For(workKind)
	eb.WithResourceDep(workKind, workControllerID)

	work := &kratix.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-work",
			Namespace: "default",
			Labels: map[string]string{
				"tracey-uid":                       "work-1",
				"discrete.events/sleeve-object-id": "work-1",
			},
		},
		Spec: kratix.WorkSpec{
			PromiseName:  "example-promise",
			ResourceName: "example-resource",
		},
	}
	work.SetGroupVersionKind(kratix.GroupVersion.WithKind("Work"))

	stateBuilder := eb.NewStateEventBuilder()
	initialState := stateBuilder.AddTopLevelObject(work, workControllerID)

	runner, err := explore.NewRunner(eb)
	if err != nil {
		panic(fmt.Sprintf("create runner: %v", err))
	}
	if err := runner.Run(context.Background(), initialState); err != nil {
		panic(fmt.Sprintf("run explorer: %v", err))
	}
}
