package v1alpha1_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var _ = Describe("PromiseRevision", func() {
	Describe("Constructor", func() {
		var promise *platformv1alpha1.Promise
		var promiseVersion string

		BeforeEach(func() {
			promise = &platformv1alpha1.Promise{
				ObjectMeta: metav1.ObjectMeta{Name: "mypromise"},
				Spec: platformv1alpha1.PromiseSpec{
					API: &runtime.RawExtension{
						Raw: []byte(`{"apiVersion":"v1","kind":"Promise","metadata":{"name":"mypromise"}}`),
					},
				},
			}
			promiseVersion = "v1.0.0"
		})

		It("generates the correct set of matchLabels", func() {
			revision := platformv1alpha1.NewPromiseRevision(promise, promiseVersion)
			Expect(revision.Name).To(Equal("mypromise-2888c"))
			Expect(revision.Labels).To(HaveKeyWithValue("kratix.io/promise-name", "mypromise"))
			Expect(revision.Spec.PromiseRef.Name).To(Equal("mypromise"))
			Expect(revision.Spec.Version).To(Equal("v1.0.0"))
		})
	})

	Describe("Label and annotation helpers", func() {
		It("sets and reads skip-resource-request-cleanup-on-delete", func() {
			revision := &platformv1alpha1.PromiseRevision{}
			Expect(revision.SkipResourceRequestCleanupOnDelete()).To(BeFalse())
			revision.SetSkipResourceRequestCleanupOnDelete()
			Expect(revision.SkipResourceRequestCleanupOnDelete()).To(BeTrue())
			Expect(revision.Annotations[platformv1alpha1.SkipResourceRequestCleanupOnDeleteAnnotation]).To(Equal(platformv1alpha1.MetadataBoolTrue))
		})

		It("sets skip annotation when annotations already exist", func() {
			revision := &platformv1alpha1.PromiseRevision{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{"other": "x"},
				},
			}
			revision.SetSkipResourceRequestCleanupOnDelete()
			Expect(revision.Annotations["other"]).To(Equal("x"))
			Expect(revision.SkipResourceRequestCleanupOnDelete()).To(BeTrue())
		})

		It("sets and clears the latest revision label", func() {
			revision := &platformv1alpha1.PromiseRevision{}
			revision.SetLatestRevisionLabel()
			Expect(revision.HasLatestRevisionLabel()).To(BeTrue())
			revision.ClearLatestRevisionLabel()
			Expect(revision.HasLatestRevisionLabel()).To(BeFalse())
		})

		It("sets latest revision label when other labels already exist", func() {
			revision := &platformv1alpha1.PromiseRevision{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{platformv1alpha1.PromiseNameLabel: "redis"},
				},
			}
			revision.SetLatestRevisionLabel()
			Expect(revision.Labels[platformv1alpha1.PromiseNameLabel]).To(Equal("redis"))
			Expect(revision.HasLatestRevisionLabel()).To(BeTrue())
		})
	})

	Describe("ReconciliationInterval", func() {
		fallback := 10 * time.Hour

		It("returns the fallback and false when the snapshot does not declare an interval", func() {
			revision := &platformv1alpha1.PromiseRevision{}
			interval, fromRevision := revision.ReconciliationInterval(fallback)
			Expect(interval).To(Equal(fallback))
			Expect(fromRevision).To(BeFalse())
		})

		It("returns the snapshot's interval and true when declared", func() {
			revision := &platformv1alpha1.PromiseRevision{
				Spec: platformv1alpha1.PromiseRevisionSpec{
					PromiseSpec: platformv1alpha1.PromiseSpec{
						Workflows: platformv1alpha1.Workflows{
							Config: platformv1alpha1.WorkflowConfig{
								ReconciliationInterval: &metav1.Duration{Duration: 3 * time.Minute},
							},
						},
					},
				},
			}
			interval, fromRevision := revision.ReconciliationInterval(fallback)
			Expect(interval).To(Equal(3 * time.Minute))
			Expect(fromRevision).To(BeTrue())
		})

		const specSnapshot = 3 * time.Minute

		revisionWithAnnotation := func(annotation string) *platformv1alpha1.PromiseRevision {
			revision := &platformv1alpha1.PromiseRevision{
				Spec: platformv1alpha1.PromiseRevisionSpec{
					PromiseSpec: platformv1alpha1.PromiseSpec{
						Workflows: platformv1alpha1.Workflows{
							Config: platformv1alpha1.WorkflowConfig{
								ReconciliationInterval: &metav1.Duration{Duration: specSnapshot},
							},
						},
					},
				},
			}
			if annotation != "" {
				revision.SetAnnotations(map[string]string{platformv1alpha1.ReconciliationIntervalAnnotation: annotation})
			}
			return revision
		}

		It("resolves to the annotation's value when it differs from the spec snapshot", func() {
			revision := revisionWithAnnotation("5m")
			interval, _ := revision.ReconciliationInterval(fallback)
			Expect(interval).To(Equal(5 * time.Minute))
		})

		It("resolves to the spec snapshot when the annotation is absent", func() {
			revision := revisionWithAnnotation("")
			interval, _ := revision.ReconciliationInterval(fallback)
			Expect(interval).To(Equal(3 * time.Minute))
		})

		It("resolves to the spec snapshot when the annotation is unparseable", func() {
			revision := revisionWithAnnotation("not-a-duration")
			interval, _ := revision.ReconciliationInterval(fallback)
			Expect(interval).To(Equal(3 * time.Minute))

			applied, declined := revision.ReconciliationIntervalAnnotation()
			Expect(applied).To(BeFalse())
			Expect(declined).To(BeTrue())
		})

		It("resolves to the spec snapshot, not the global default, when the annotation is below the floor", func() {
			revision := revisionWithAnnotation("30s")
			interval, _ := revision.ReconciliationInterval(fallback)
			Expect(interval).To(Equal(3 * time.Minute))

			applied, declined := revision.ReconciliationIntervalAnnotation()
			Expect(applied).To(BeFalse())
			Expect(declined).To(BeTrue())
		})

		It("reports neither applied nor declined when the annotation is absent", func() {
			revision := revisionWithAnnotation("")
			applied, declined := revision.ReconciliationIntervalAnnotation()
			Expect(applied).To(BeFalse())
			Expect(declined).To(BeFalse())
		})

		It("reports applied and not declined when the annotation is valid", func() {
			revision := revisionWithAnnotation("5m")
			applied, declined := revision.ReconciliationIntervalAnnotation()
			Expect(applied).To(BeTrue())
			Expect(declined).To(BeFalse())
		})
	})
})
