package v1alpha1_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
)

func RawExtension(a interface{}) runtime.RawExtension {
	b, err := json.Marshal(a)
	Expect(err).NotTo(HaveOccurred())
	return runtime.RawExtension{Raw: b}
}

var _ = Describe("PromiseWebhook", func() {
	var baseCRD, newCRD *v1.CustomResourceDefinition
	var oldPromise *v1alpha1.Promise

	newPromise := func() *v1alpha1.Promise {
		return &v1alpha1.Promise{
			Spec: v1alpha1.PromiseSpec{
				API: RawExtension(newCRD),
			},
		}
	}

	BeforeEach(func() {
		fakeClientSet := fake.NewSimpleClientset()
		v1alpha1.SetClientSet(fakeClientSet)
		baseCRD = &v1.CustomResourceDefinition{
			TypeMeta: metav1.TypeMeta{
				Kind:       "CustomResourceDefinition",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "mycrds.group.example",
			},
			Spec: v1.CustomResourceDefinitionSpec{
				Group: "group.example",
				Names: v1.CustomResourceDefinitionNames{
					Plural:   "mycrds",
					Singular: "mycrd",
					Kind:     "MyCRD",
				},
			},
		}
		oldPromise = &v1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{
				Name: "mypromise",
			},
			Spec: v1alpha1.PromiseSpec{
				API: RawExtension(baseCRD),
			},
		}

		newCRD = &v1.CustomResourceDefinition{}
		*newCRD = *baseCRD
	})

	When("Updating Immutable fields", func() {
		It("returns list of errors containing all changed fields", func() {
			newCRD.Name = "mynewcrds.group.example"
			newCRD.Kind = "NotACRD"
			newCRD.APIVersion = "v2"
			newCRD.Spec.Names.Kind = "NewKind"

			warnings, err := newPromise().ValidateUpdate(oldPromise)
			Expect(warnings).To(BeEmpty())
			Expect(err.Error()).To(SatisfyAll(
				ContainSubstring("spec.api.metadata.name"),
				ContainSubstring("spec.api.kind"),
				ContainSubstring("spec.api.apiVersion"),
				ContainSubstring("spec.api.spec.names"),
			))
		})
	})
})
