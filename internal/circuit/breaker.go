package circuit

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
)

// State is the breaker state for a single resource key.
type State int

const (
	StateClosed State = iota
	StateHalfOpen
	StateOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	default:
		return "unknown"
	}
}

// BreakerParams configures a Breaker. Zero values are NOT safe defaults;
// callers should populate every field.
type BreakerParams struct {
	Burst                 float64
	RefillRate            float64 // tokens per second
	Cooldown              time.Duration
	HalfOpenProbeInterval time.Duration
	Disabled              bool
}

// Breaker gates enqueue decisions and tracks per-resource state.
// All methods are safe for concurrent use.
type Breaker interface {
	Allow(key types.NamespacedName) bool
	Observe(key types.NamespacedName, success bool)
	State(key types.NamespacedName) State
	UpdateParams(params BreakerParams)
}

// NewTokenBucketBreaker constructs a Breaker. clk may be nil to use a real clock.
func NewTokenBucketBreaker(params BreakerParams, clk clock.Clock) Breaker {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &tokenBucketBreaker{
		params: params,
		clock:  clk,
		keys:   map[types.NamespacedName]*bucketState{},
	}
}

type bucketState struct {
	tokens        float64
	lastRefill    time.Time
	state         State
	openedAt      time.Time
	lastProbe     time.Time
	inFlightProbe bool
}

type tokenBucketBreaker struct {
	mu     sync.Mutex
	params BreakerParams
	clock  clock.Clock
	keys   map[types.NamespacedName]*bucketState
}

func (b *tokenBucketBreaker) Allow(key types.NamespacedName) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.params.Disabled {
		return true
	}

	now := b.clock.Now()
	st, ok := b.keys[key]
	if !ok {
		st = &bucketState{
			tokens:     b.params.Burst,
			lastRefill: now,
			state:      StateClosed,
		}
		b.keys[key] = st
	}

	// Refill.
	elapsed := now.Sub(st.lastRefill).Seconds()
	if elapsed > 0 {
		st.tokens = min(b.params.Burst, st.tokens+elapsed*b.params.RefillRate)
		st.lastRefill = now
	}

	switch st.state {
	case StateOpen:
		if now.Sub(st.openedAt) < b.params.Cooldown {
			return false
		}
		// Cooldown elapsed → move to half-open. Mark the probe as in-flight so
		// a slow Reconcile cannot trigger overlapping probes before Observe lands.
		st.state = StateHalfOpen
		st.lastProbe = now
		st.inFlightProbe = true
		return true
	case StateHalfOpen:
		// Only one probe is allowed in flight at a time. A parallel Allow call
		// while a probe is outstanding short-circuits to false.
		if st.inFlightProbe {
			return false
		}
		if now.Sub(st.lastProbe) < b.params.HalfOpenProbeInterval {
			return false
		}
		st.lastProbe = now
		st.inFlightProbe = true
		return true
	}

	if st.tokens < 1 {
		st.state = StateOpen
		st.openedAt = now
		return false
	}
	st.tokens--
	return true
}

func (b *tokenBucketBreaker) Observe(key types.NamespacedName, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.keys[key]
	if !ok {
		return
	}
	// Always clear the in-flight probe flag so the next probe can fire,
	// regardless of which terminal branch we hit below.
	defer func() {
		if entry, present := b.keys[key]; present {
			entry.inFlightProbe = false
		}
	}()
	if st.state != StateHalfOpen {
		return
	}
	if success {
		// Drop the key entirely on recovery. A fresh Allow recreates the bucket
		// with full burst — semantically identical to "reset to closed" but
		// bounds the map to "currently misbehaving" keys for long-running operators.
		delete(b.keys, key)
		return
	}
	st.state = StateOpen
	st.openedAt = b.clock.Now()
}

func (b *tokenBucketBreaker) State(key types.NamespacedName) State {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.keys[key]
	if !ok {
		return StateClosed
	}
	return st.state
}

func (b *tokenBucketBreaker) UpdateParams(params BreakerParams) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.params = params
}
