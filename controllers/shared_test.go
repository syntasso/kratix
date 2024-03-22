package controllers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/lib/workflow"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	kubebuilder "sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	promisePath                    = "assets/redis-simple-promise.yaml"
	promiseWithWorkflowPath        = "assets/promise-with-workflow.yaml"
	promiseWithDeleteWorkflowPath  = "assets/promise-with-delete-workflow.yaml"
	promiseWithWorkflowUpdatedPath = "assets/promise-with-workflow-updated.yaml"
	promiseWithOnlyDepsPath        = "assets/promise-with-deps-only.yaml"
	promiseWithOnlyDepsUpdatedPath = "assets/promise-with-deps-only-updated.yaml"
	promiseWithRequirements        = "assets/promise-with-requirements.yaml"
	resourceRequestPath            = "assets/redis-request.yaml"
	resourceRequestUpdatedPath     = "assets/redis-request-updated.yaml"

	updatedPromisePath = "assets/redis-simple-promise-updated.yaml"
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

func conditionsFromStatus(status interface{}) ([]clusterv1.Condition, error) {
	conditions := []clusterv1.Condition{}

	statusMap := status.(map[string]interface{})
	conditionsMap, ok := statusMap["conditions"].([]interface{})
	if !ok {
		return conditions, fmt.Errorf("invalid status format")
	}

	for _, rawCondition := range conditionsMap {
		conditionData, ok := rawCondition.(map[string]interface{})
		if !ok {
			return conditions, fmt.Errorf("invalid condition data format")
		}

		conditionBytes, err := json.Marshal(conditionData)
		if err != nil {
			return conditions, err
		}

		var condition clusterv1.Condition
		if err := json.Unmarshal(conditionBytes, &condition); err != nil {
			return conditions, err
		}

		conditions = append(conditions, condition)
	}

	return conditions, nil
}

// doesn't need to be reset, just need an int going up every call

// Creating the work to mimic the pipelines behaviour.
func autoCompleteJobAndCreateWork(labels map[string]string, workName string) func(client.Object) error {
	return func(obj client.Object) error {
		controllers.SetReconcileConfigurePipeline(func(w workflow.Opts) (bool, error) {
			reconcileConfigurePipelineArg = w
			return true, nil
		})

		controllers.SetReconcileDeletePipeline(func(w workflow.Opts, p workflow.Pipeline) (bool, error) {
			us := &unstructured.Unstructured{}
			us.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
				Name:      obj.GetName(),
				Namespace: obj.GetNamespace(),
			}, us)).To(Succeed())

			controllerutil.RemoveFinalizer(us, "kratix.io/delete-workflows")
			Expect(fakeK8sClient.Update(ctx, us)).To(Succeed())

			reconcileDeletePipelineManagerArg = w
			reconcileDeletePipelineArg = p
			return true, nil
		})

		return nil
	}
	// return func(obj client.Object) error {
	//callCount++
	//jobs := &batchv1.JobList{}
	//Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
	//if len(jobs.Items) == 0 {
	//	return nil
	//}

	//for _, j := range jobs.Items {
	//	job := &batchv1.Job{}
	//	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
	//		Name:      j.GetName(),
	//		Namespace: j.GetNamespace(),
	//	}, job)).To(Succeed())

	//	if len(job.Status.Conditions) > 0 {
	//		continue
	//	}

	//	//Fake library doesn't set timestamp, and we need it set for comparing age
	//	//of jobs. This ensures its set once, and only when its first created, and
	//	//that they differ by a large enough amont (time.Now() alone was not enough)
	//	job.CreationTimestamp = metav1.NewTime(time.Now().Add(time.Duration(callCount) * time.Minute))
	//	err := fakeK8sClient.Update(ctx, job)
	//	if err != nil {
	//		return err
	//	}

	//	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
	//		Name:      j.GetName(),
	//		Namespace: j.GetNamespace(),
	//	}, job)).To(Succeed())

	//	job.Status.Conditions = []batchv1.JobCondition{
	//		{
	//			Type:   batchv1.JobComplete,
	//			Status: v1.ConditionTrue,
	//		},
	//	}
	//	job.Status.Succeeded = 1

	//	err = fakeK8sClient.Status().Update(ctx, job)
	//	if err != nil {
	//		return err
	//	}

	//	namespace := obj.GetNamespace()
	//	if obj.GetNamespace() == "" {
	//		namespace = v1alpha1.SystemNamespace
	//	}

	//	Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(Succeed())
	//	fakeK8sClient.Create(ctx, &v1alpha1.Work{
	//		ObjectMeta: metav1.ObjectMeta{
	//			Name:      workName,
	//			Namespace: namespace,
	//			Labels:    labels,
	//		},
	//	})

	//}
	//return nil
	// }
}
