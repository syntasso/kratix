package controller_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/internal/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func rrWith(setters ...func(*unstructured.Unstructured)) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetName("rr-1")
	u.SetNamespace("default")
	u.SetGeneration(1)
	u.SetFinalizers([]string{"kratix.io/work-cleanup"})
	u.SetLabels(map[string]string{"kratix.io/promise-name": "perftest"})
	for _, s := range setters {
		s(u)
	}
	return u
}

func withStatus(status map[string]any) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		u.Object["status"] = status
	}
}

var _ = Describe("DynamicRRNoOpWriteFilter", func() {
	var p interface {
		Update(event.UpdateEvent) bool
		Create(event.CreateEvent) bool
		Delete(event.DeleteEvent) bool
		Generic(event.GenericEvent) bool
	}

	BeforeEach(func() {
		p = controller.DynamicRRNoOpWriteFilter()
	})

	It("filters identical updates", func() {
		old := rrWith(withStatus(map[string]any{"phase": "Running"}))
		cur := rrWith(withStatus(map[string]any{"phase": "Running"}))
		Expect(p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur})).To(BeFalse())
	})

	It("passes generation bump", func() {
		old := rrWith()
		cur := rrWith(func(u *unstructured.Unstructured) { u.SetGeneration(2) })
		Expect(p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur})).To(BeTrue())
	})

	It("passes finalizer change", func() {
		old := rrWith()
		cur := rrWith(func(u *unstructured.Unstructured) {
			u.SetFinalizers([]string{"kratix.io/work-cleanup", "kratix.io/workflows-cleanup"})
		})
		Expect(p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur})).To(BeTrue())
	})

	It("passes label change", func() {
		old := rrWith()
		cur := rrWith(func(u *unstructured.Unstructured) {
			u.SetLabels(map[string]string{"kratix.io/promise-name": "perftest", "kratix.io/manual-reconciliation": "true"})
		})
		Expect(p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur})).To(BeTrue())
	})

	It("passes annotation change", func() {
		old := rrWith()
		cur := rrWith(func(u *unstructured.Unstructured) {
			u.SetAnnotations(map[string]string{"some.annotation": "v"})
		})
		Expect(p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur})).To(BeTrue())
	})

	It("passes deletion timestamp transition", func() {
		old := rrWith()
		cur := rrWith(func(u *unstructured.Unstructured) {
			now := metav1.NewTime(time.Now())
			u.SetDeletionTimestamp(&now)
		})
		Expect(p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur})).To(BeTrue())
	})

	It("passes status content change", func() {
		old := rrWith(withStatus(map[string]any{"phase": "Pending"}))
		cur := rrWith(withStatus(map[string]any{"phase": "Running"}))
		Expect(p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur})).To(BeTrue())
	})

	It("passes lastTransitionTime change", func() {
		old := rrWith(withStatus(map[string]any{
			"conditions": []any{map[string]any{"type": "Reconciled", "status": "True", "lastTransitionTime": "2026-01-01T00:00:00Z"}},
		}))
		cur := rrWith(withStatus(map[string]any{
			"conditions": []any{map[string]any{"type": "Reconciled", "status": "True", "lastTransitionTime": "2026-01-01T00:00:01Z"}},
		}))
		Expect(p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur})).To(BeTrue())
	})

	It("passes Create, Delete, and Generic events", func() {
		obj := rrWith()
		Expect(p.Create(event.CreateEvent{Object: obj})).To(BeTrue())
		Expect(p.Delete(event.DeleteEvent{Object: obj})).To(BeTrue())
		Expect(p.Generic(event.GenericEvent{Object: obj})).To(BeTrue())
	})

	Context("when objects are not unstructured", func() {
		It("defensively passes when old is nil", func() {
			Expect(p.Update(event.UpdateEvent{ObjectOld: nil, ObjectNew: rrWith()})).To(BeTrue())
		})

		It("defensively passes when new is nil", func() {
			Expect(p.Update(event.UpdateEvent{ObjectOld: rrWith(), ObjectNew: nil})).To(BeTrue())
		})
	})
})
