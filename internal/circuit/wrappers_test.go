package circuit_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/syntasso/kratix/internal/circuit"
)

var _ = Describe("MapFunc wrapper", func() {
	var (
		clk     *testclock.FakeClock
		breaker circuit.Breaker
		inner   handler.MapFunc
		gated   handler.MapFunc
	)

	BeforeEach(func() {
		clk = testclock.NewFakeClock(time.Unix(0, 0))
		breaker = circuit.NewTokenBucketBreaker(circuit.BreakerParams{
			Burst:      2,
			RefillRate: 0.1,
			Cooldown:   time.Minute,
		}, clk)
		inner = func(_ context.Context, _ client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rr"}}}
		}
		gated = circuit.MapFunc(inner, breaker)
	})

	It("passes through requests while the breaker is closed", func() {
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		Expect(gated(context.Background(), obj)).To(HaveLen(1))
	})

	It("drops requests once the breaker opens", func() {
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		// First two pass, third drained.
		gated(context.Background(), obj)
		gated(context.Background(), obj)
		Expect(gated(context.Background(), obj)).To(BeEmpty())
	})

	It("returns inner result unchanged when inner emits nothing", func() {
		emptyInner := func(_ context.Context, _ client.Object) []reconcile.Request { return nil }
		gated := circuit.MapFunc(emptyInner, breaker)
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		Expect(gated(context.Background(), obj)).To(BeNil())
	})
})

var _ = Describe("Predicate wrapper", func() {
	var (
		clk     *testclock.FakeClock
		breaker circuit.Breaker
		pred    interface {
			Create(event.CreateEvent) bool
			Update(event.UpdateEvent) bool
			Delete(event.DeleteEvent) bool
			Generic(event.GenericEvent) bool
		}
	)

	BeforeEach(func() {
		clk = testclock.NewFakeClock(time.Unix(0, 0))
		breaker = circuit.NewTokenBucketBreaker(circuit.BreakerParams{
			Burst:      1,
			RefillRate: 0.01,
			Cooldown:   time.Minute,
		}, clk)
		pred = circuit.Predicate(breaker)
	})

	It("allows Create when breaker is closed and drops once open", func() {
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		Expect(pred.Create(event.CreateEvent{Object: obj})).To(BeTrue())
		Expect(pred.Create(event.CreateEvent{Object: obj})).To(BeFalse())
	})

	It("always allows Delete events even when the breaker is open", func() {
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		// Trip open.
		pred.Create(event.CreateEvent{Object: obj})
		pred.Create(event.CreateEvent{Object: obj})

		Expect(pred.Delete(event.DeleteEvent{Object: obj})).To(BeTrue())
	})
})
