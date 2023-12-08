package controllers_test

import (
	"context"
	"fmt"
	"os"

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

const promisePath = "assets/redis-simple-promise.yaml"
const updatedPromisePath = "assets/redis-simple-promise-updated.yaml"

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
	singleReconcile bool
}

func reconcile(r kubebuilder.Reconciler, obj client.Object, opts ...*opts) (ctrl.Result, error) {
	k8sObj := &unstructured.Unstructured{}
	k8sObj.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())

	namespacedName := types.NamespacedName{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	Expect(fakeK8sClient.Get(context.Background(), namespacedName, k8sObj)).To(Succeed())

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: namespacedName})
	if err != nil || len(opts) > 0 && opts[0].singleReconcile {
		return result, err
	}

	if k8sObj.GetResourceVersion() == "20" { // arbitrary number to stop infinite loops
		return ctrl.Result{}, fmt.Errorf("reconcile loop detected")
	}

	newK8sObj := &unstructured.Unstructured{}
	newK8sObj.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
	err = fakeK8sClient.Get(context.Background(), namespacedName, newK8sObj)
	if err != nil {
		if errors.IsNotFound(err) {
			return result, nil
		}
		return ctrl.Result{}, err
	}

	if k8sObj.GetResourceVersion() == newK8sObj.GetResourceVersion() {
		return result, nil
	}

	return reconcile(r, obj, opts...)
}
