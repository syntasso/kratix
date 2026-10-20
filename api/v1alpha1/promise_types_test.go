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
	It("discards legacy workflow status while preserving keyed workflows", func() {
		status := map[string]any{"status": map[string]any{"kratix": map[string]any{"workflows": map[string]any{
			"pipelines":           []any{map[string]any{"name": "old", "phase": "Succeeded"}},
			"suspendedGeneration": int64(1),
			"example":             map[string]any{"pipelines": []any{map[string]any{"name": "current", "phase": "Succeeded", "hash": "abc"}}},
		}}}}
		promise := &platformv1alpha1.Promise{}
		Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(status, promise)).To(Succeed())
		Expect(promise.Status.Kratix.Workflows).To(HaveLen(1))
		Expect(promise.Status.Kratix.Workflows["example"].Pipelines[0].Hash).To(Equal("abc"))
	})

	Describe("Status", func() {
		var statusWithCustomFields map[string]any

		BeforeEach(func() {
			statusWithCustomFields = map[string]any{"status": map[string]any{
				"message": "Promise configured",
				"childResources": []any{
					map[string]any{"kind": "Database", "name": "db-1"},
				},
				"owner": "team-a",
			}}
		})

		It("keeps custom fields through JSON", func() {
			data, err := json.Marshal(statusWithCustomFields)
			Expect(err).NotTo(HaveOccurred())

			promise := &platformv1alpha1.Promise{}
			Expect(json.Unmarshal(data, promise)).To(Succeed())
			Expect(promise.Status.Message).To(Equal("Promise configured"))

			promise.Status.Message = "updated by kratix"
			roundTripped, err := json.Marshal(promise)
			Expect(err).NotTo(HaveOccurred())

			result := map[string]any{}
			Expect(json.Unmarshal(roundTripped, &result)).To(Succeed())
			Expect(result["status"]).To(Equal(map[string]any{
				"message": "updated by kratix",
				"childResources": []any{
					map[string]any{"kind": "Database", "name": "db-1"},
				},
				"owner":  "team-a",
				"kratix": map[string]any{},
			}))
		})

		It("keeps custom fields through the unstructured converter", func() {
			promise := &platformv1alpha1.Promise{}
			Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(statusWithCustomFields, promise)).To(Succeed())

			promise.Status.Kind = "Database"
			result, err := runtime.DefaultUnstructuredConverter.ToUnstructured(promise)
			Expect(err).NotTo(HaveOccurred())

			status := result["status"].(map[string]any)
			Expect(status).To(HaveKeyWithValue("kind", "Database"))
			Expect(status).To(HaveKeyWithValue("owner", "team-a"))
			Expect(status).To(HaveKey("childResources"))
		})

		It("keeps custom fields through DeepCopy", func() {
			promise := &platformv1alpha1.Promise{}
			Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(statusWithCustomFields, promise)).To(Succeed())

			copied := promise.DeepCopy()
			data, err := json.Marshal(copied)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring(`"owner":"team-a"`))
		})

		It("lets Kratix-owned fields win over custom fields with the same name", func() {
			promise := &platformv1alpha1.Promise{}
			promise.Status.Custom = map[string]apiextensionsv1.JSON{
				"message": {Raw: []byte(`"from custom"`)},
			}
			promise.Status.Message = "from kratix"

			data, err := json.Marshal(&promise.Status)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring(`"message":"from kratix"`))
			Expect(string(data)).NotTo(ContainSubstring("from custom"))
		})

		It("only lets workflows write message and conditions among the typed Promise status fields", func() {
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("message")).To(BeFalse())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("conditions")).To(BeFalse())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("my-custom-status")).To(BeFalse())

			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("version")).To(BeTrue())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("kind")).To(BeTrue())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("apiVersion")).To(BeTrue())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("status")).To(BeTrue())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("requiredPromises")).To(BeTrue())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("requiredBy")).To(BeTrue())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("lastAvailableTime")).To(BeTrue())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("observedGeneration")).To(BeTrue())
			Expect(platformv1alpha1.IsKratixOwnedPromiseStatusField("kratix")).To(BeTrue())
		})
	})

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

		When("promise has no api", func() {
			When("the promise api is nil or invalid", func() {
				It("errors", func() {
					promise.Spec.API.Raw = nil
					_, _, err := promise.GetAPI()
					Expect(err).To(MatchError(platformv1alpha1.ErrNoAPI))
				})

				It("errors", func() {
					promise.Spec.API = nil
					_, _, err := promise.GetAPI()
					Expect(err).To(MatchError(platformv1alpha1.ErrNoAPI))
				})

				It("errors", func() {
					promise.Spec.API.Raw = []byte("{}")
					_, _, err := promise.GetAPI()
					Expect(err).To(MatchError(platformv1alpha1.ErrNoAPI))
				})

				It("errors", func() {
					setPromiseAPI(promise, nil)
					_, _, err := promise.GetAPI()
					Expect(err).To(MatchError(platformv1alpha1.ErrNoAPI))
				})
			})
		})
	})
})

func setPromiseAPI(p *platformv1alpha1.Promise, crd *apiextensionsv1.CustomResourceDefinition) {
	crdBytes, err := json.Marshal(crd)
	Expect(err).ToNot(HaveOccurred())
	p.Spec.API = &runtime.RawExtension{Raw: crdBytes}
}
