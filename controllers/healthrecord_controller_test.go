package controllers_test

import (
	"fmt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

var _ = Describe("HealthRecordController", func() {
	var (
		healthRecord *v1alpha1.HealthRecord
		promise      *v1alpha1.Promise
		resource     *unstructured.Unstructured
		reconciler   *controllers.HealthRecordReconciler
	)

	reconcile := func() *unstructured.Unstructured {
		result, err := t.reconcileUntilCompletion(reconciler, healthRecord)
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		updatedResource := &unstructured.Unstructured{}
		updatedResource.SetKind(resource.GetKind())
		updatedResource.SetAPIVersion(resource.GetAPIVersion())

		err = fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updatedResource)
		Expect(err).ToNot(HaveOccurred())

		return updatedResource
	}

	BeforeEach(func() {
		promise = createPromise(promisePath)
		resource = createResourceRequest(resourceRequestPath)

		details := &runtime.RawExtension{Raw: []byte(`{"info":"message"}`)}

		healthRecord = &v1alpha1.HealthRecord{
			TypeMeta: metav1.TypeMeta{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "HealthRecord",
			},
			ObjectMeta: metav1.ObjectMeta{Name: "a-name", Namespace: "default"},
			Data: v1alpha1.HealthRecordData{
				PromiseRef:  v1alpha1.PromiseRef{Name: promise.GetName()},
				ResourceRef: v1alpha1.ResourceRef{Name: resource.GetName(), Namespace: resource.GetNamespace()},
				State:       "healthy",
				LastRun:     fmt.Sprintf("%d", time.Now().Unix()),
				Details:     details,
			},
		}

		reconciler = &controllers.HealthRecordReconciler{
			Client: fakeK8sClient,
			Scheme: scheme.Scheme,
			Log:    GinkgoLogr,
		}

		Expect(fakeK8sClient.Create(ctx, healthRecord)).To(Succeed())
	})

	It("updates the resource status.healthRecord with the HealthRecord data", func() {
		updatedResource := reconcile()

		status := getResourceStatus(updatedResource)

		record, found := status["healthRecord"]
		Expect(found).To(BeTrue(), "healthrecord key not found in status")
		Expect(record).To(SatisfyAll(
			HaveKeyWithValue("state", healthRecord.Data.State),
			HaveKeyWithValue("details", HaveKeyWithValue("info", "message")),
		))
	})

	When("the resource already has some status", func() {
		BeforeEach(func() {
			statusMap := map[string]interface{}{
				"some": "status",
				"nested": map[string]interface{}{
					"value": "data",
				},
			}
			Expect(unstructured.SetNestedMap(resource.Object, statusMap, "status")).To(Succeed())
			Expect(fakeK8sClient.Status().Update(ctx, resource)).To(Succeed())
		})

		It("won't overwrite the existing status", func() {
			updatedResource := reconcile()

			status := getResourceStatus(updatedResource)

			Expect(status).To(SatisfyAll(
				HaveKeyWithValue("some", "status"),
				HaveKeyWithValue("nested", HaveKeyWithValue("value", "data")),
				HaveKeyWithValue("healthRecord", HaveKeyWithValue("state", healthRecord.Data.State)),
			))
		})
	})
})

func getResourceStatus(r *unstructured.Unstructured) map[string]interface{} {
	status, foundHealthRecord, err := unstructured.NestedMap(r.Object, "status")
	Expect(err).ToNot(HaveOccurred())
	Expect(foundHealthRecord).To(BeTrue())
	return status
}
