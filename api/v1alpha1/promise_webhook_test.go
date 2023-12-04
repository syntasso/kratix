package v1alpha1_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/syntasso/kratix/api/v1alpha1"
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

	newPromise := func() *v1alpha1.Promise {
		return &v1alpha1.Promise{
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
		v1alpha1.SetClientSet(fakeClientSet)
		v1alpha1.SetClient(fakeClient)

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

	When("the promise has no API", func() {
		It("returns no error", func() {
			promise := &v1alpha1.Promise{
				ObjectMeta: metav1.ObjectMeta{
					Name: "mypromise",
				},
				Spec: v1alpha1.PromiseSpec{
					API: RawExtension(nil),
				},
			}
			warnings, err := promise.ValidateCreate()
			Expect(warnings).To(BeEmpty())
			Expect(err).NotTo(HaveOccurred())
		})
	})

	When("Requirements", func() {
		When("the promise requirements are not satisfied", func() {
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
						API: RawExtension(nil),
						Requires: []v1alpha1.Requirement{
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

				warnings, err := promise.ValidateCreate()
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(ConsistOf(
					`Requirement Promise "redis" at version "v1.0.0" not installed`,
					`Requirement Promise "kafka" installed but not at a compatible version, want: "v1.2.0" have: "v1.0.0"`,
					`Promise will not be available until the above issue(s) is resolved`,
				))

				warnings, err = promise.ValidateUpdate(promise)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(ConsistOf(
					`Requirement Promise "redis" at version "v1.0.0" not installed`,
					`Requirement Promise "kafka" installed but not at a compatible version, want: "v1.2.0" have: "v1.0.0"`,
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
						API: RawExtension(nil),
						Requires: []v1alpha1.Requirement{
							{
								Name:    "kafka",
								Version: "v1.2.0",
							},
						},
					},
				}

				warnings, err := promise.ValidateCreate()
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(BeEmpty())

				warnings, err = promise.ValidateUpdate(promise)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(BeEmpty())
			})
		})
	})
})
