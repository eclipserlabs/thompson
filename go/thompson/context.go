package thompson

import (
	"math/rand/v2"
	"sync"
)

// Context selects a bandit partition.
type Context interface {
	PartitionKey() string
}

// SimpleContext is a string-bucketed context (task, tenant).
type SimpleContext string

func (s SimpleContext) PartitionKey() string { return string(s) }

// NoContext is single partition — preserves non-contextual behavior.
type NoContext struct{}

func (NoContext) PartitionKey() string { return "default" }

// PartitionedPolicy is N independent Policies keyed by Context.
type PartitionedPolicy struct {
	mu             sync.Mutex
	partitions     map[string]*Policy
	config         Config
	samplerFactory func() Sampler
}

// NewPartitionedPolicy creates a partitioned policy with sampler factory per partition.
func NewPartitionedPolicy(config Config, factory func() Sampler) *PartitionedPolicy {
	return &PartitionedPolicy{
		partitions:     make(map[string]*Policy),
		config:         config,
		samplerFactory: factory,
	}
}

func (pp *PartitionedPolicy) ensureLocked(ctx Context) *Policy {
	key := ctx.PartitionKey()
	if _, ok := pp.partitions[key]; !ok {
		pp.partitions[key] = New(pp.config, pp.samplerFactory())
	}
	return pp.partitions[key]
}

// AddArmIn registers arm in a specific partition.
func (pp *PartitionedPolicy) AddArmIn(ctx Context, id string) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	pp.ensureLocked(ctx).AddArm(id)
}

// Select in context.
func (pp *PartitionedPolicy) Select(ctx Context, rng *rand.Rand) (string, error) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return pp.ensureLocked(ctx).Select(rng)
}

// Record in context.
func (pp *PartitionedPolicy) Record(ctx Context, rng *rand.Rand, id string, reward float64) error {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return pp.ensureLocked(ctx).Record(rng, id, reward)
}

// LenPartitions returns number of partitions.
func (pp *PartitionedPolicy) LenPartitions() int {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return len(pp.partitions)
}
