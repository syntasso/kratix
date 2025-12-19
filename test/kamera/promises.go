package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	kratix "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/tgoodwin/kamera/pkg/explore"
	"github.com/tgoodwin/kamera/pkg/tag"
	"github.com/tgoodwin/kamera/pkg/tracecheck"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type defaultNamespaceClient struct {
	ctrlclient.Client
	namespace string
}

func (c *defaultNamespaceClient) Get(ctx context.Context, key ctrlclient.ObjectKey, obj ctrlclient.Object, opts ...ctrlclient.GetOption) error {
	if shouldDefaultNamespace(obj) && key.Namespace == "" {
		key.Namespace = c.namespace
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *defaultNamespaceClient) Create(ctx context.Context, obj ctrlclient.Object, opts ...ctrlclient.CreateOption) error {
	defaultNamespace(obj, c.namespace)
	return c.Client.Create(ctx, obj, opts...)
}

func (c *defaultNamespaceClient) Update(ctx context.Context, obj ctrlclient.Object, opts ...ctrlclient.UpdateOption) error {
	defaultNamespace(obj, c.namespace)
	return c.Client.Update(ctx, obj, opts...)
}

func (c *defaultNamespaceClient) Patch(ctx context.Context, obj ctrlclient.Object, patch ctrlclient.Patch, opts ...ctrlclient.PatchOption) error {
	defaultNamespace(obj, c.namespace)
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (c *defaultNamespaceClient) Status() ctrlclient.SubResourceWriter {
	return &defaultNamespaceStatusWriter{
		SubResourceWriter: c.Client.Status(),
		namespace:         c.namespace,
	}
}

type defaultNamespaceStatusWriter struct {
	ctrlclient.SubResourceWriter
	namespace string
}

func (w *defaultNamespaceStatusWriter) Update(ctx context.Context, obj ctrlclient.Object, opts ...ctrlclient.SubResourceUpdateOption) error {
	defaultNamespace(obj, w.namespace)
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

func (w *defaultNamespaceStatusWriter) Patch(ctx context.Context, obj ctrlclient.Object, patch ctrlclient.Patch, opts ...ctrlclient.SubResourcePatchOption) error {
	defaultNamespace(obj, w.namespace)
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

func shouldDefaultNamespace(obj ctrlclient.Object) bool {
	switch obj.(type) {
	case *kratix.Promise, *kratix.PromiseRevision:
		return true
	default:
		return false
	}
}

func defaultNamespace(obj ctrlclient.Object, namespace string) {
	if shouldDefaultNamespace(obj) && obj.GetNamespace() == "" {
		obj.SetNamespace(namespace)
	}
}

func main() {
	flag.Parse()

	scheme := runtime.NewScheme()
	utilruntime.Must(kratix.AddToScheme(scheme))
	utilruntime.Must(kratix.AddToScheme(clientgoscheme.Scheme))

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	tracecheck.SetLogger(ctrl.Log.WithName("tracecheck"))

	const promiseControllerID = "PromiseController"
	const promiseRevisionControllerID = "PromiseRevisionController"
	promiseKind := "platform.kratix.io/Promise"
	promiseRevisionKind := "platform.kratix.io/PromiseRevision"

	eb := tracecheck.NewExplorerBuilder(scheme)
	eb.WithMaxDepth(100)
	eb.WithReconciler(promiseControllerID, func(c ctrlclient.Client) tracecheck.Reconciler {
		nsClient := &defaultNamespaceClient{Client: c, namespace: "default"}
		return &controller.PromiseReconciler{
			Client:                 nsClient,
			Log:                    ctrl.Log.WithName("promise"),
			EventRecorder:          record.NewFakeRecorder(32),
			PromiseUpgrade:         true,
			NumberOfJobsToKeep:     1,
			ReconciliationInterval: time.Hour,
		}
	}).For(promiseKind)
	eb.WithReconciler(promiseRevisionControllerID, func(c ctrlclient.Client) tracecheck.Reconciler {
		nsClient := &defaultNamespaceClient{Client: c, namespace: "default"}
		return &controller.PromiseRevisionReconciler{
			Client:        nsClient,
			Log:           ctrl.Log.WithName("promise-revision"),
			EventRecorder: record.NewFakeRecorder(32),
		}
	}).For(promiseRevisionKind)
	eb.WithResourceDep(promiseKind, promiseControllerID)
	eb.WithResourceDep(promiseRevisionKind, promiseRevisionControllerID)

	promise := &kratix.Promise{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-promise",
			Namespace: "default",
			Labels: map[string]string{
				kratix.PromiseVersionLabel: "v1",
			},
		},
	}
	promise.SetGroupVersionKind(kratix.GroupVersion.WithKind("Promise"))
	tag.AddSleeveObjectID(promise)

	initialState, err := eb.BuildStartStateFromObjects(
		[]ctrlclient.Object{promise},
		[]tracecheck.PendingReconcile{
			{
				ReconcilerID: promiseControllerID,
				Request: ctrl.Request{
					NamespacedName: ctrlclient.ObjectKeyFromObject(promise),
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
