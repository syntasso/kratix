package v1alpha1_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/ptr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var _ = Describe("Promise", func() {
	Describe("Scheduling", func() {
		It("generates the correct set of matchLabels", func() {
			input := []platformv1alpha1.PromiseScheduling{
				{
					MatchLabels: map[string]string{
						"environment": "dev",
					},
				},
				{
					MatchLabels: map[string]string{
						"environment": "prod",
						"pci":         "false",
					},
				},
				{
					MatchLabels: map[string]string{
						"pci":    "true",
						"secure": "false",
					},
				},
			}

			promise := platformv1alpha1.Promise{
				Spec: platformv1alpha1.PromiseSpec{
					DestinationSelectors: input,
				},
			}

			selectors := promise.GetSchedulingSelectors()
			Expect(labels.FormatLabels(selectors)).To(Equal(`environment=dev,pci=false,secure=false`))
		})
	})

	Describe("GetAPI", func() {
		var crd *apiextensionsv1.CustomResourceDefinition
		var promise *platformv1alpha1.Promise

		BeforeEach(func() {
			crd = &apiextensionsv1.CustomResourceDefinition{
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "promise.crd.group",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Singular: "promiseCrd",
						Plural:   "promiseCrdPlural",
						Kind:     "PromiseCrd",
					},
					Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
						Name: "v1",
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Type: "object",
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									"apiVersion": {Type: "string"},
									"kind":       {Type: "string"},
									"metadata": {
										Type: "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{
											"name":      {Type: "string", Description: "the name of the resource"},
											"namespace": {Type: "string"},
										},
									},
									"spec": {
										Type: "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{
											"replicas": {Type: "integer"},
											"image":    {Type: "string"},
										},
									},
								},
							},
						},
					}},
				},
			}

			promise = &platformv1alpha1.Promise{
				ObjectMeta: metav1.ObjectMeta{
					Name: "myPromise",
				},
			}
			setPromiseAPI(promise, crd)
		})

		It("returns the gvk", func() {
			gvk, _, _ := promise.GetAPI()
			Expect(*gvk).To(Equal(schema.GroupVersionKind{
				Group:   "promise.crd.group",
				Version: "v1",
				Kind:    "PromiseCrd",
			}))
		})

		It("sets the maxLength on the metadata.name property", func() {
			_, c, _ := promise.GetAPI()
			Expect(c.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["metadata"]).To(Equal(
				apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"name":      {Type: "string", MaxLength: ptr.To(int64(63)), Description: "the name of the resource"},
						"namespace": {Type: "string"},
					},
				}))
		})

		When("the crd does not define metadata", func() {
			It("defines it and sets the maxLength", func() {
				delete(crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties, "metadata")
				setPromiseAPI(promise, crd)

				_, c, _ := promise.GetAPI()
				Expect(c.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties).To(HaveKeyWithValue(
					"metadata",
					apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"name": {Type: "string", MaxLength: ptr.To(int64(63))},
						},
					},
				))
			})
		})

	})
})

func setPromiseAPI(p *platformv1alpha1.Promise, crd *apiextensionsv1.CustomResourceDefinition) {
	crdBytes, err := json.Marshal(crd)
	Expect(err).ToNot(HaveOccurred())
	p.Spec.API = &runtime.RawExtension{Raw: crdBytes}
}
