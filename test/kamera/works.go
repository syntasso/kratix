package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/go-logr/logr"
	kratix "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/tgoodwin/kamera/pkg/explore"
	"github.com/tgoodwin/kamera/pkg/tag"
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

type fakeStateStoreWriter struct {
	updates int
}

func (w *fakeStateStoreWriter) UpdateFiles(string, string, []kratix.Workload, []string) (string, error) {
	w.updates++
	return fmt.Sprintf("fake-%d", w.updates), nil
}

func (*fakeStateStoreWriter) ReadFile(string) ([]byte, error) {
	return nil, writers.ErrFileNotFound
}

func (*fakeStateStoreWriter) ValidatePermissions() error {
	return nil
}

func init() {
	controller.SetStateStoreWriterFactories(
		func(logr.Logger, kratix.BucketStateStoreSpec, string, map[string][]byte) (writers.StateStoreWriter, error) {
			return &fakeStateStoreWriter{}, nil
		},
		func(logr.Logger, kratix.GitStateStoreSpec, string, map[string][]byte) (writers.StateStoreWriter, error) {
			return &fakeStateStoreWriter{}, nil
		},
	)
}

func main() {
	flag.Parse()

	scheme := runtime.NewScheme()
	utilruntime.Must(kratix.AddToScheme(scheme))

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	tracecheck.SetLogger(ctrl.Log.WithName("tracecheck"))

	const workControllerID = "WorkController"
	const workPlacementControllerID = "WorkPlacementController"
	workKind := "platform.kratix.io/Work"
	workPlacementKind := "platform.kratix.io/WorkPlacement"
	destinationKind := "platform.kratix.io/Destination"
	stateStoreKind := "platform.kratix.io/BucketStateStore"

	eb := tracecheck.NewExplorerBuilder(scheme)
	eb.WithMaxDepth(100)
	eb.WithReconciler(workControllerID, func(c ctrlclient.Client) tracecheck.Reconciler {
		return &controller.WorkReconciler{
			Client:        c,
			Log:           ctrl.Log.WithName("work"),
			Scheduler:     noopScheduler{},
			EventRecorder: record.NewFakeRecorder(32),
		}
	}).For(workKind)
	eb.WithReconciler(workPlacementControllerID, func(c ctrlclient.Client) tracecheck.Reconciler {
		return &controller.WorkPlacementReconciler{
			Client:        c,
			Log:           ctrl.Log.WithName("workplacement"),
			VersionCache:  map[string]string{},
			EventRecorder: record.NewFakeRecorder(32),
		}
	}).For(workPlacementKind)
	eb.WithResourceDep(workKind, workControllerID)
	eb.WithResourceDep(workPlacementKind, workPlacementControllerID)
	eb.WithResourceDep(destinationKind, workPlacementControllerID)
	eb.WithResourceDep(stateStoreKind, workPlacementControllerID)

	work := &kratix.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-work",
			Namespace: "default",
		},
		Spec: kratix.WorkSpec{
			PromiseName:  "example-promise",
			ResourceName: "example-resource",
		},
	}
	work.SetGroupVersionKind(kratix.GroupVersion.WithKind("Work"))
	tag.AddSleeveObjectID(work)

	compressed, err := compression.CompressContent([]byte("kind: ConfigMap\napiVersion: v1\n"))
	if err != nil {
		panic(fmt.Sprintf("compress workload: %v", err))
	}

	workPlacement := &kratix.WorkPlacement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-workplacement",
			Namespace: "default",
			Labels: map[string]string{
				"kratix.io/work":          work.Name,
				"kratix.io/pipeline-name": "default",
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: kratix.GroupVersion.String(),
					Kind:       "Work",
					Name:       work.Name,
				},
			},
		},
		Spec: kratix.WorkPlacementSpec{
			TargetDestinationName: "example-destination",
			PromiseName:           work.Spec.PromiseName,
			ResourceName:          work.Spec.ResourceName,
			ID:                    "workload-group-1",
			Workloads: []kratix.Workload{
				{
					Filepath: "manifests/configmap.yaml",
					Content:  string(compressed),
				},
			},
		},
	}
	workPlacement.SetGroupVersionKind(kratix.GroupVersion.WithKind("WorkPlacement"))
	tag.AddSleeveObjectID(workPlacement)

	destination := &kratix.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example-destination",
		},
		Spec: kratix.DestinationSpec{
			Path: "example-path",
			StateStoreRef: &kratix.StateStoreReference{
				Kind: "BucketStateStore",
				Name: "example-statestore",
			},
		},
	}
	destination.SetGroupVersionKind(kratix.GroupVersion.WithKind("Destination"))
	tag.AddSleeveObjectID(destination)

	stateStore := &kratix.BucketStateStore{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example-statestore",
		},
		Spec: kratix.BucketStateStoreSpec{
			BucketName: "example-bucket",
			Endpoint:   "https://example.invalid",
			AuthMethod: kratix.AuthMethodIAM,
		},
	}
	stateStore.SetGroupVersionKind(kratix.GroupVersion.WithKind("BucketStateStore"))
	tag.AddSleeveObjectID(stateStore)

	initialState, err := eb.BuildStartStateFromObjects(
		[]ctrlclient.Object{work, workPlacement, destination, stateStore},
		[]tracecheck.PendingReconcile{
			{
				ReconcilerID: workPlacementControllerID,
				Request: ctrl.Request{
					NamespacedName: ctrlclient.ObjectKeyFromObject(workPlacement),
				},
				Source: tracecheck.SourceStateChange,
			},
			{
				ReconcilerID: workControllerID,
				Request: ctrl.Request{
					NamespacedName: ctrlclient.ObjectKeyFromObject(work),
				},
				Source: tracecheck.SourceStateChange,
			},
		},
	)
	if err != nil {
		panic(fmt.Sprintf("build start state: %v", err))
	}

	runner, err := explore.NewRunner(eb)
	if err != nil {
		panic(fmt.Sprintf("create runner: %v", err))
	}
	if err := runner.Run(context.Background(), initialState); err != nil {
		panic(fmt.Sprintf("run explorer: %v", err))
	}
}
