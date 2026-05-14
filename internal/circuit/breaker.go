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
	tokens     float64
	lastRefill time.Time
	state      State
	openedAt   time.Time
	lastProbe  time.Time
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
		st.tokens = minFloat(b.params.Burst, st.tokens+elapsed*b.params.RefillRate)
		st.lastRefill = now
	}

	switch st.state {
	case StateOpen:
		if now.Sub(st.openedAt) < b.params.Cooldown {
			return false
		}
		// Cooldown elapsed → move to half-open.
		st.state = StateHalfOpen
		st.lastProbe = now
		return true
	case StateHalfOpen:
		if now.Sub(st.lastProbe) < b.params.HalfOpenProbeInterval {
			return false
		}
		st.lastProbe = now
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
	if st.state != StateHalfOpen {
		return
	}
	if success {
		st.state = StateClosed
		st.tokens = b.params.Burst
		st.lastRefill = b.clock.Now()
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

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
