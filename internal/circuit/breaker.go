package circuit

import (
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
	// unexported fields filled in Task 2
	params BreakerParams
	clock  clock.Clock
	keys   map[types.NamespacedName]*bucketState
}

// Stub method bodies to satisfy the interface; real logic in Task 2.
func (b *tokenBucketBreaker) Allow(key types.NamespacedName) bool       { return true }
func (b *tokenBucketBreaker) Observe(key types.NamespacedName, ok bool) {}
func (b *tokenBucketBreaker) State(key types.NamespacedName) State      { return StateClosed }
func (b *tokenBucketBreaker) UpdateParams(params BreakerParams)         {}
