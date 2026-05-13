package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// rrWith returns a minimal unstructured RR with the supplied metadata + status
// shape. It's deliberately not a generic helper — the predicate is the only
// caller and the fixtures are small.
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

func TestDynamicRRNoOpWriteFilter_filtersIdenticalUpdate(t *testing.T) {
	t.Parallel()
	p := dynamicRRNoOpWriteFilter()
	old := rrWith(withStatus(map[string]any{"phase": "Running"}))
	cur := rrWith(withStatus(map[string]any{"phase": "Running"}))
	if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur}) {
		t.Fatalf("expected predicate to filter identical update")
	}
}

func TestDynamicRRNoOpWriteFilter_passesGenerationChange(t *testing.T) {
	t.Parallel()
	p := dynamicRRNoOpWriteFilter()
	old := rrWith()
	cur := rrWith(func(u *unstructured.Unstructured) { u.SetGeneration(2) })
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur}) {
		t.Fatalf("expected predicate to pass generation bump")
	}
}

func TestDynamicRRNoOpWriteFilter_passesFinalizerChange(t *testing.T) {
	t.Parallel()
	p := dynamicRRNoOpWriteFilter()
	old := rrWith()
	cur := rrWith(func(u *unstructured.Unstructured) {
		u.SetFinalizers([]string{"kratix.io/work-cleanup", "kratix.io/workflows-cleanup"})
	})
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur}) {
		t.Fatalf("expected predicate to pass finalizer change")
	}
}

func TestDynamicRRNoOpWriteFilter_passesLabelChange(t *testing.T) {
	t.Parallel()
	p := dynamicRRNoOpWriteFilter()
	old := rrWith()
	cur := rrWith(func(u *unstructured.Unstructured) {
		u.SetLabels(map[string]string{"kratix.io/promise-name": "perftest", "kratix.io/manual-reconciliation": "true"})
	})
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur}) {
		t.Fatalf("expected predicate to pass label change")
	}
}

func TestDynamicRRNoOpWriteFilter_passesAnnotationChange(t *testing.T) {
	t.Parallel()
	p := dynamicRRNoOpWriteFilter()
	old := rrWith()
	cur := rrWith(func(u *unstructured.Unstructured) {
		u.SetAnnotations(map[string]string{"some.annotation": "v"})
	})
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur}) {
		t.Fatalf("expected predicate to pass annotation change")
	}
}

func TestDynamicRRNoOpWriteFilter_passesDeletionTimestamp(t *testing.T) {
	t.Parallel()
	p := dynamicRRNoOpWriteFilter()
	old := rrWith()
	cur := rrWith(func(u *unstructured.Unstructured) {
		now := metav1.NewTime(time.Now())
		u.SetDeletionTimestamp(&now)
	})
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur}) {
		t.Fatalf("expected predicate to pass deletion timestamp transition")
	}
}

func TestDynamicRRNoOpWriteFilter_passesStatusContentChange(t *testing.T) {
	t.Parallel()
	p := dynamicRRNoOpWriteFilter()
	old := rrWith(withStatus(map[string]any{"phase": "Pending"}))
	cur := rrWith(withStatus(map[string]any{"phase": "Running"}))
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur}) {
		t.Fatalf("expected predicate to pass status content change")
	}
}

func TestDynamicRRNoOpWriteFilter_passesLastTransitionTimeChange(t *testing.T) {
	t.Parallel()
	// A status mutation that only flips a condition's lastTransitionTime is
	// still a content change under DeepEqual — the predicate must let it
	// through. This is the conservative behaviour documented on the predicate.
	p := dynamicRRNoOpWriteFilter()
	old := rrWith(withStatus(map[string]any{
		"conditions": []any{map[string]any{"type": "Reconciled", "status": "True", "lastTransitionTime": "2026-01-01T00:00:00Z"}},
	}))
	cur := rrWith(withStatus(map[string]any{
		"conditions": []any{map[string]any{"type": "Reconciled", "status": "True", "lastTransitionTime": "2026-01-01T00:00:01Z"}},
	}))
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: cur}) {
		t.Fatalf("expected predicate to pass lastTransitionTime change")
	}
}

func TestDynamicRRNoOpWriteFilter_passesCreateDeleteGeneric(t *testing.T) {
	t.Parallel()
	p := dynamicRRNoOpWriteFilter()
	obj := rrWith()
	if !p.Create(event.CreateEvent{Object: obj}) {
		t.Fatalf("expected Create to pass")
	}
	if !p.Delete(event.DeleteEvent{Object: obj}) {
		t.Fatalf("expected Delete to pass")
	}
	if !p.Generic(event.GenericEvent{Object: obj}) {
		t.Fatalf("expected Generic to pass")
	}
}

func TestDynamicRRNoOpWriteFilter_defensivePassOnNonUnstructured(t *testing.T) {
	t.Parallel()
	// If for any reason ObjectOld or ObjectNew is not an unstructured RR (a
	// shouldn't-happen case for a dynamic CRD watch), the predicate must
	// fail safe by letting the event through rather than silently dropping.
	p := dynamicRRNoOpWriteFilter()
	if !p.Update(event.UpdateEvent{ObjectOld: nil, ObjectNew: rrWith()}) {
		t.Fatalf("expected predicate to defensively pass when old is nil")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: rrWith(), ObjectNew: nil}) {
		t.Fatalf("expected predicate to defensively pass when new is nil")
	}
}
