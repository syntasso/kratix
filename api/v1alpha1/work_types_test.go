package v1alpha1_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/syntasso/kratix/api/v1alpha1"
)

type testGroup struct {
	dependencies v1alpha1.Dependencies
	selector     map[string]string
}

var _ = Describe("WorkTypes", func() {
	When("the dependencies contain destination overrides", func() {
		It("create multiple workload groups in the work", func() {
			dep1 := v1alpha1.Dependency{
				newDependency("dep1", ""),
			}
			dep2 := v1alpha1.Dependency{
				// but ok yaml?
				// yeah, no quotes needed for yaml keys
				newDependency("cep2", "{matchLabels: {some: label, environment: dev}}"),
			}
			dep3 := v1alpha1.Dependency{
				newDependency("dep3", "{matchLabels: {environment: dev, some: label}}"),
			}
			dep4 := v1alpha1.Dependency{
				newDependency("dep4", ""),
			}
			dep5 := v1alpha1.Dependency{
				newDependency("dep5", "{matchLabels: {environment: prod}}"),
			}
			promise := &v1alpha1.Promise{
				ObjectMeta: metav1.ObjectMeta{Name: "promise-name"},
				Spec: v1alpha1.PromiseSpec{
					Dependencies: []v1alpha1.Dependency{dep1, dep2, dep3, dep4, dep5},
				},
			}

			work, err := v1alpha1.NewPromiseDependenciesWork(promise)

			groups := []testGroup{
				{dependencies: []v1alpha1.Dependency{dep1, dep4}, selector: nil},
				{dependencies: []v1alpha1.Dependency{dep2, dep3}, selector: map[string]string{"environment": "dev", "some": "label"}},
				{dependencies: []v1alpha1.Dependency{dep5}, selector: map[string]string{"environment": "prod"}},
			}

			Expect(err).ToNot(HaveOccurred())
			Expect(work.Spec.WorkloadGroups).To(HaveLen(len(groups)))

			for i, group := range groups {
				workload := work.Spec.WorkloadGroups[i]
				Expect(workload.Workloads).To(HaveLen(1)) // all dependencies are bundled into a single file

				if group.selector == nil {
					Expect(workload.DestinationSelectorsOverride).To(BeNil())
				} else {
					for key, value := range group.selector {
						Expect(workload.DestinationSelectorsOverride.Promise[0].MatchLabels).To(
							HaveKeyWithValue(key, value),
						)
					}
				}

				for _, dep := range group.dependencies {
					Expect(workload.WorkloadCoreFields.Workloads[0].Content).To(ContainSubstring(dep.GetName()))
				}

			}
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
			v1alpha1.DestinationSelectorsOverride: override,
		})
	}

	return *u
}
