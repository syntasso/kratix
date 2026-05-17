package workflow

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func makeServiceAccount(name string, labels map[string]string) *corev1.ServiceAccount {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kratix-platform-system",
			Labels:    labels,
		},
	}
	sa.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ServiceAccount"})
	return sa
}

func TestSharedResourceCache_AppliedHitsAndMisses(t *testing.T) {
	c := NewSharedResourceCache()
	sa := makeServiceAccount("kratix-sa", nil)

	if c.Applied("scope", sa) {
		t.Fatalf("expected cache miss before MarkApplied")
	}
	c.MarkApplied("scope", sa)
	if !c.Applied("scope", sa) {
		t.Fatalf("expected cache hit after MarkApplied")
	}
	if c.Applied("other-scope", sa) {
		t.Fatalf("different scope must not hit")
	}
}

func TestSharedResourceCache_SpecChangeMissesCache(t *testing.T) {
	c := NewSharedResourceCache()
	original := makeServiceAccount("kratix-sa", map[string]string{"v": "1"})
	c.MarkApplied("scope", original)

	updated := makeServiceAccount("kratix-sa", map[string]string{"v": "2"})
	if c.Applied("scope", updated) {
		t.Fatalf("expected cache miss after spec change so apply is retried")
	}
}

func TestSharedResourceCache_EntryExpires(t *testing.T) {
	c := NewSharedResourceCache()
	now := time.Now()
	c.now = func() time.Time { return now }

	sa := makeServiceAccount("kratix-sa", nil)
	c.MarkApplied("scope", sa)
	if !c.Applied("scope", sa) {
		t.Fatalf("expected hit immediately after MarkApplied")
	}

	c.now = func() time.Time { return now.Add(c.ttl + time.Second) }
	if c.Applied("scope", sa) {
		t.Fatalf("expected cache miss after TTL expiry so out-of-band deletion is recovered")
	}
}

func TestSharedResourceCache_NilSafe(t *testing.T) {
	var c *SharedResourceCache
	sa := makeServiceAccount("kratix-sa", nil)
	if c.Applied("scope", sa) {
		t.Fatalf("nil receiver Applied must return false")
	}
	c.MarkApplied("scope", sa) // must not panic
}
