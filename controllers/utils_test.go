package controllers_test

import (
	"context"
	"fmt"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubebuilder "sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	promisePath         = "assets/redis-simple-promise.yaml"
	promiseWithWorkflow = "assets/promise-with-workflow.yaml"
	updatedPromisePath  = "assets/redis-simple-promise-updated.yaml"
)

func promiseFromFile(path string) *v1alpha1.Promise {
	promiseBody, err := os.Open(path)
	Expect(err).ToNot(HaveOccurred())

	decoder := yaml.NewYAMLOrJSONDecoder(promiseBody, 2048)
	promise := &v1alpha1.Promise{}
	err = decoder.Decode(promise)
	Expect(err).ToNot(HaveOccurred())
	promiseBody.Close()

	return promise
}

func fetchPromise(namespacedName types.NamespacedName) *v1alpha1.Promise {
	promise := &v1alpha1.Promise{}
	err := fakeK8sClient.Get(context.TODO(), namespacedName, promise)
	Expect(err).ToNot(HaveOccurred())
	return promise
}

func deletePromise(namespacedName types.NamespacedName) {
	// The fakeClient will return 404 if the object has deletionTimestamp and no Finalizers
	promise := fetchPromise(namespacedName)

	promise.SetFinalizers([]string{})
	Expect(fakeK8sClient.Update(context.TODO(), promise)).To(Succeed())

	fakeK8sClient.Delete(context.TODO(), promise)
}

type opts struct {
	//Reconcile only once
	singleReconcile bool
	// Functions to run on the object before each reconcile
	funcs []func(client.Object) error
	// Number of errors to tolerate before failing
	errorBudget int
}

type testReconciler struct {
	// Number of errors that have occurred, not to be set by the caller
	errorCount int
	// Number of times to reconcile
	reconcileCount int
}

// Run the reconciler until all these are satisfied:
// - reconciler is not returning a requeuing result and no error
// - the resource is not updated after a reconcile
// TODO: We watch for various other resources to trigger reconciliaton loop,
// e.g. changes to jobs owned by a promise trigger the promise. Need to improve
// this to handle that
func (t *testReconciler) reconcileUntilCompletion(r kubebuilder.Reconciler, obj client.Object, opts ...*opts) (ctrl.Result, error) {
	t.reconcileCount++
	k8sObj := &unstructured.Unstructured{}
	k8sObj.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())

	namespacedName := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	err := fakeK8sClient.Get(context.Background(), namespacedName, k8sObj)
	if err != nil {
		GinkgoWriter.Write([]byte("resource doesn't exist, reconciling 1 last time"))
		return r.Reconcile(context.Background(), ctrl.Request{NamespacedName: namespacedName})
	}

	if len(opts) > 0 {
		for _, f := range opts[0].funcs {
			err := f(k8sObj)
			if err != nil {
				GinkgoWriter.Write([]byte(fmt.Sprintf("error in func: %v\n", err)))
			}
		}
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: namespacedName})
	if err != nil {
		if len(opts) > 0 && t.errorCount <= opts[0].errorBudget {
			// Some errors can naturally occur, e.g. race conditions between gets/deletes is okay.
			t.errorCount++
			fmt.Println("reconcile1")
			return t.reconcileUntilCompletion(r, obj, opts...)
		}
		return result, err
	}

	if len(opts) > 0 && opts[0].singleReconcile {
		return result, err
	}

	if t.reconcileCount > 30 { // arbitrary number to stop infinite loops
		//reset so func can be run again
		t.reconcileCount = 0
		t.errorCount = 0
		return ctrl.Result{}, fmt.Errorf("reconcile loop detected")
	}

	newK8sObj := &unstructured.Unstructured{}
	newK8sObj.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
	err = fakeK8sClient.Get(context.Background(), namespacedName, newK8sObj)
	if err != nil {
		if errors.IsNotFound(err) {
			// The object was deleted, so we need to requeue one last time to mimick
			// the k8s api behaviou
			return r.Reconcile(context.Background(), ctrl.Request{NamespacedName: namespacedName})
		}
		return ctrl.Result{}, err
	}

	if int64(result.RequeueAfter) == 0 && k8sObj.GetResourceVersion() == newK8sObj.GetResourceVersion() {
		return result, nil
	}

	return t.reconcileUntilCompletion(r, obj, opts...)
}
