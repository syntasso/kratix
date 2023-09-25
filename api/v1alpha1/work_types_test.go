package v1alpha1_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/syntasso/kratix/api/v1alpha1"
)

var _ = Describe("WorkTypes", func() {
	When("the dependencies contain destination overrides", func() {
		It("create multiple workload groups in the work", func() {
			dep1 := v1alpha1.Dependency{
				newDependency("foo", ""),
			}
			dep2 := v1alpha1.Dependency{
				newDependency("bar", "{matchLabels: {environment: dev}}"),
			}
			dep3 := v1alpha1.Dependency{
				newDependency("new", "{matchLabels: {environment: dev}}"),
			}
			dep4 := v1alpha1.Dependency{
				newDependency("yay", ""),
			}
			promise := &v1alpha1.Promise{
				ObjectMeta: metav1.ObjectMeta{Name: "promise-name"},
				Spec: v1alpha1.PromiseSpec{
					Dependencies: []v1alpha1.Dependency{dep1, dep2, dep3, dep4},
				},
			}

			work, err := v1alpha1.NewPromiseDependenciesWork(promise)

			Expect(err).ToNot(HaveOccurred())
			Expect(work.Spec.WorkloadGroups).To(HaveLen(2))
			Expect(work.Spec.WorkloadGroups[0].Workloads).To(HaveLen(1))
			//todo: assert on both contents being inside the depednency file
			Expect(work.Spec.WorkloadGroups[1].Workloads).To(HaveLen(1))
		})
	})
})

func newDependency(name, override string) unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("some.api.version/v1")
	u.SetKind("someKind")
	u.SetName(name)
	u.SetNamespace("default")
	if override != "" {
		u.SetAnnotations(map[string]string{
			"kratix.io/destination-selectors-override": override,
		})
	}

	return *u
}
