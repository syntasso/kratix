package v1alpha1_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/syntasso/kratix/api/v1alpha1"
	kratixWebhook "github.com/syntasso/kratix/internal/webhook/v1alpha1"
)

func RawExtension(a interface{}) *runtime.RawExtension {
	b, err := json.Marshal(a)
	Expect(err).NotTo(HaveOccurred())
	return &runtime.RawExtension{Raw: b}
}

var _ = Describe("PromiseWebhook", func() {
	var baseCRD, newCRD *v1.CustomResourceDefinition
	var oldPromise *v1alpha1.Promise
	var fakeClient client.Client
	var validator *kratixWebhook.PromiseCustomValidator

	ctx := context.TODO()
	newPromise := func() *v1alpha1.Promise {
		return &v1alpha1.Promise{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Promise",
				APIVersion: v1alpha1.GroupVersion.Group + "/" + v1alpha1.GroupVersion.Version,
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "mypromise",
			},
			Spec: v1alpha1.PromiseSpec{
				API: RawExtension(newCRD),
			},
		}
	}

	BeforeEach(func() {
		fakeClientSet := fake.NewSimpleClientset()
		err := v1alpha1.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())
		fakeClient = clientfake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		kratixWebhook.SetClientSet(fakeClientSet)
		kratixWebhook.SetClient(fakeClient)
		validator = &kratixWebhook.PromiseCustomValidator{}

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
				Scope: v1.NamespaceScoped,
				Names: v1.CustomResourceDefinitionNames{
					Plural:   "mycrds",
					Singular: "mycrd",
					Kind:     "MyCRD",
				},
				Versions: []v1.CustomResourceDefinitionVersion{
					{
						Name: "v1",
					},
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
	Context("spec.api", func() {
		When("updating Immutable fields", func() {
			It("returns list of errors containing all changed fields", func() {
				newCRD.Name = "mynewcrds.group.example"
				newCRD.Kind = "NotACRD"
				newCRD.APIVersion = "v2"
				newCRD.Spec.Names.Kind = "NewKind"
				newP := newPromise()

				warnings, err := validator.ValidateUpdate(ctx, newP, oldPromise)
				Expect(warnings).To(BeEmpty())
				Expect(err.Error()).To(SatisfyAll(
					ContainSubstring("spec.api.metadata.name"),
					ContainSubstring("spec.api.kind"),
					ContainSubstring("spec.api.apiVersion"),
					ContainSubstring("spec.api.spec.names"),
				))
			})
		})

		When("the promise has no API", func() {
			It("returns no error", func() {
				promise := &v1alpha1.Promise{
					ObjectMeta: metav1.ObjectMeta{
						Name: "mypromise",
					},
					Spec: v1alpha1.PromiseSpec{
						API: nil,
					},
				}
				warnings, err := validator.ValidateCreate(ctx, promise)
				Expect(warnings).To(BeEmpty())
				Expect(err).NotTo(HaveOccurred())
			})
		})

		When("the scope is set to cluster scoped", func() {
			It("returns an error saying kratix support namespaced CRD only", func() {
				baseCRD.Spec.Scope = v1.ClusterScoped
				promise := &v1alpha1.Promise{
					ObjectMeta: metav1.ObjectMeta{
						Name: "mypromise",
					},
					Spec: v1alpha1.PromiseSpec{
						API: RawExtension(baseCRD),
					},
				}

				_, err := validator.ValidateCreate(ctx, promise)
				Expect(err).To(MatchError("promise api needs to be namespace scoped; spec.api.spec.scope cannot be: Cluster"))
			})
		})
	})

	Describe("Pipeline", func() {
		var promise *v1alpha1.Promise

		BeforeEach(func() {
			promise = newPromise()
		})

		When("the pipeline is not of kind 'Pipeline' or apiGroup 'platform.kratix.io'", func() {
			It("returns an error", func() {
				objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test"}})
				Expect(err).NotTo(HaveOccurred())
				unstructuredPipeline := &unstructured.Unstructured{Object: objMap}
				unstructuredPipeline.SetAPIVersion("v1")
				unstructuredPipeline.SetKind("ConfigMap")
				promise.Spec.Workflows.Resource.Configure = []unstructured.Unstructured{*unstructuredPipeline}
				_, err = validator.ValidateCreate(ctx, promise)
				Expect(err).To(MatchError(ContainSubstring(`unsupported pipeline "test" with APIVersion "ConfigMap/v1"`)))
			})
		})

		When("multiple pipelines within the same workflow and action have the same name", func() {
			It("errors", func() {
				promise = newPromise()
				pipeline := v1alpha1.Pipeline{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
				}
				objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipeline)
				Expect(err).NotTo(HaveOccurred())
				unstructuredPipeline := &unstructured.Unstructured{Object: objMap}
				unstructuredPipeline.SetAPIVersion("platform.kratix.io/v1alpha1")
				unstructuredPipeline.SetKind("Pipeline")
				promise.Spec.Workflows.Resource.Configure = []unstructured.Unstructured{*unstructuredPipeline, *unstructuredPipeline}
				_, err = validator.ValidateCreate(ctx, promise)
				Expect(err).To(MatchError("duplicate pipeline name \"foo\" in workflow \"resource\" action \"configure\""))
			})
		})

		Context("Name", func() {
			var maxLimit int
			BeforeEach(func() {
				maxLimit = 60 - len(promise.Name+"-resource-configure-")
			})

			It("returns an error it is too long", func() {
				pipeline := v1alpha1.Pipeline{
					ObjectMeta: metav1.ObjectMeta{
						Name: randomishString(maxLimit + 1),
					},
				}
				objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipeline)
				Expect(err).NotTo(HaveOccurred())
				unstructuredPipeline := &unstructured.Unstructured{Object: objMap}
				unstructuredPipeline.SetAPIVersion("platform.kratix.io/v1alpha1")
				unstructuredPipeline.SetKind("Pipeline")
				promise.Spec.Workflows.Resource.Configure = []unstructured.Unstructured{*unstructuredPipeline}
				_, err = validator.ValidateCreate(ctx, promise)
				Expect(err).To(MatchError("resource.configure pipeline with name \"" + pipeline.GetName() + "\" is too long. " +
					"The name is used when generating resources for the pipeline,including the ServiceAccount which follows the format of " +
					"\"mypromise-resource-configure-" + pipeline.GetName() + "\", which cannot be longer than 60 characters in total"))
			})

			It("succeeds when it is within the character limit", func() {
				pipeline := v1alpha1.Pipeline{
					ObjectMeta: metav1.ObjectMeta{
						Name: randomishString(maxLimit),
					},
				}
				objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipeline)
				Expect(err).NotTo(HaveOccurred())
				unstructuredPipeline := &unstructured.Unstructured{Object: objMap}
				unstructuredPipeline.SetAPIVersion("platform.kratix.io/v1alpha1")
				unstructuredPipeline.SetKind("Pipeline")
				promise.Spec.Workflows.Resource.Configure = []unstructured.Unstructured{*unstructuredPipeline}
				_, err = validator.ValidateCreate(ctx, promise)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		DescribeTable("validates the provided labels", func(key, val, expectedErr string) {
			pipeline := v1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pipeline-name",
					Labels: map[string]string{
						key: val,
					},
				},
			}
			setPipeline(promise, pipeline)
			warnings, err := validator.ValidateCreate(ctx, promise)
			Expect(warnings).To(BeEmpty())
			if expectedErr != "" {
				Expect(err).To(MatchError(ContainSubstring(expectedErr)))
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
			Entry("by not erroring for valid labels", "app.kubernetes.io/name", "test-label-123", ""),
			Entry("by erroring for non-conforming label values", "labelKey", "a bad label", `invalid label value "a bad label"`),
			Entry("by erroring for non-conforming label keys", "invalid key", "valid-value", `invalid label key "invalid key"`),
		)

		When("the pipeline has invalid fields", func() {
			It("errors", func() {
				promise = newPromise()
				promise.Spec.Workflows.Resource.Configure = []unstructured.Unstructured{
					{
						Object: map[string]interface{}{
							"apiVersion": "platform.kratix.io/v1alpha1",
							"kind":       "Pipeline",
							"metadata": map[string]interface{}{
								"namespace": "default",
								"name":      "pipeline1",
							},
							"spec": map[string]interface{}{
								"containers": []map[string]interface{}{
									{
										"name":         "promise-configure",
										"image":        "my-registry.io/configure",
										"non-existing": true,
									},
								},
							},
						},
					},
				}
				_, err := validator.ValidateCreate(ctx, promise)
				Expect(err).To(MatchError("failed parsing resource.configure pipeline: failed unmarshalling pipeline pipeline1: json: unknown field \"non-existing\""))
			})
		})

	})

	When("Required Promises", func() {
		When("the required promises are not satisfied", func() {
			It("returns a list of warnings", func() {
				err := fakeClient.Create(context.TODO(), &v1alpha1.Promise{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kafka",
					},
					Status: v1alpha1.PromiseStatus{
						Version: "v1.0.0",
					},
				})
				Expect(err).NotTo(HaveOccurred())
				promise := &v1alpha1.Promise{
					ObjectMeta: metav1.ObjectMeta{
						Name: "mypromise",
					},
					Spec: v1alpha1.PromiseSpec{
						API: nil,
						RequiredPromises: []v1alpha1.RequiredPromise{
							{
								Name:    "redis",
								Version: "v1.0.0",
							},
							{
								Name:    "kafka",
								Version: "v1.2.0",
							},
						},
					},
				}

				warnings, err := validator.ValidateCreate(ctx, promise)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(ConsistOf(
					`Required Promise "redis" at version "v1.0.0" not installed`,
					`Required Promise "kafka" installed but not at a compatible version, want: "v1.2.0" have: "v1.0.0"`,
					`Promise will not be available until the above issue(s) is resolved`,
				))

				warnings, err = validator.ValidateCreate(ctx, promise)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(ConsistOf(
					`Required Promise "redis" at version "v1.0.0" not installed`,
					`Required Promise "kafka" installed but not at a compatible version, want: "v1.2.0" have: "v1.0.0"`,
					`Promise will not be available until the above issue(s) is resolved`,
				))
			})
		})

		When("the dependencies are installed at the defined versions", func() {
			It("returns no errors", func() {
				err := fakeClient.Create(context.TODO(), &v1alpha1.Promise{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kafka",
					},
					Status: v1alpha1.PromiseStatus{

						Version: "v1.2.0",
					},
				})
				Expect(err).NotTo(HaveOccurred())
				promise := &v1alpha1.Promise{
					ObjectMeta: metav1.ObjectMeta{
						Name: "mypromise",
					},
					Spec: v1alpha1.PromiseSpec{
						API: nil,
						RequiredPromises: []v1alpha1.RequiredPromise{
							{
								Name:    "kafka",
								Version: "v1.2.0",
							},
						},
					},
				}

				warnings, err := validator.ValidateCreate(ctx, promise)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(BeEmpty())

				warnings, err = validator.ValidateUpdate(ctx, promise, promise)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(BeEmpty())
			})
		})
	})
})

func randomishString(length int) string {
	bufLen := 1 + length>>1 // half of length plus 1
	buf := make([]byte, bufLen)
	for i := range bufLen {
		buf[i] = byte(rand.IntN(255))
	}
	return fmt.Sprintf("%x", buf)[:length]
}

func setPipeline(promise *v1alpha1.Promise, pipeline v1alpha1.Pipeline) {
	objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipeline)
	Expect(err).NotTo(HaveOccurred())
	unstructuredPipeline := unstructured.Unstructured{Object: objMap}
	unstructuredPipeline.SetAPIVersion("platform.kratix.io/v1alpha1")
	unstructuredPipeline.SetKind("Pipeline")
	promise.Spec.Workflows.Resource.Configure = []unstructured.Unstructured{unstructuredPipeline}
}
