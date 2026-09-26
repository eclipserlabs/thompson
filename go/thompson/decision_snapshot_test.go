package thompson

import (
	"maps"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
)

func snapshotRNG() *rand.Rand { return rand.New(rand.NewPCG(0x5EED, 0xBEEF)) }

// SelectSnapshot must draw the identical decision as the legacy separate-call
// path on the same RNG stream: same chosen arm, same scores. This pins the
// "no algorithm change" requirement of the atomic-snapshot fix.
func TestSelectSnapshotMatchesLegacyPath(t *testing.T) {
	configs := map[string]Config{
		"thompson": DefaultConfig(),
		"ucb": func() Config {
			c := DefaultConfig()
			c.Selection = Selection{Kind: UCBRegularized, C: 2.0, UntilPulls: 30}
			return c
		}(),
		"phased-forced": func() Config {
			c := DefaultConfig()
			c.Selection = Selection{Kind: PhasedSelection, Bootstrap: 5, MinPullsForExploit: 5}
			return c
		}(),
		"phased-exploit": func() Config {
			c := DefaultConfig()
			c.Selection = Selection{Kind: PhasedSelection, Bootstrap: 1, MinPullsForExploit: 1}
			return c
		}(),
	}
	for name, cfg := range configs {
		t.Run(name, func(t *testing.T) {
			build := func() *Policy {
				p := New(cfg, ExactSampler{})
				p.AddArm("a")
				p.AddArm("b")
				p.AddArm("c")
				return p
			}
			legacy := build()
			rngL := snapshotRNG()
			eligibleL := legacy.EligibleArmIDs()
			chosenL, scoresL, err := legacy.SelectWithScores(rngL)
			if err != nil {
				t.Fatalf("legacy select: %v", err)
			}

			fixed := build()
			snap, err := fixed.SelectSnapshot(snapshotRNG())
			if err != nil {
				t.Fatalf("SelectSnapshot: %v", err)
			}
			if snap.Selected != chosenL {
				t.Fatalf("chosen diverged: snapshot=%q legacy=%q", snap.Selected, chosenL)
			}
			if !maps.Equal(snap.Scores, scoresL) {
				t.Fatalf("scores diverged:\nsnapshot=%v\nlegacy=%v", snap.Scores, scoresL)
			}
			if !slices.Equal(snap.Eligible, eligibleL) {
				t.Fatalf("eligible diverged: %v vs %v", snap.Eligible, eligibleL)
			}
			for _, id := range snap.Eligible {
				want, _ := legacy.PosteriorFor(id)
				if snap.Posteriors[id] != want {
					t.Fatalf("posterior for %q diverged: %+v vs %+v", id, snap.Posteriors[id], want)
				}
			}
			if snap.ConfigHash != legacy.ConfigHash() {
				t.Fatalf("config hash diverged: %q vs %q", snap.ConfigHash, legacy.ConfigHash())
			}
			if snap.TotalPulls != legacy.TotalPulls() {
				t.Fatalf("total pulls diverged: %d vs %d", snap.TotalPulls, legacy.TotalPulls())
			}
		})
	}
}

func TestSelectSnapshotEmptyPolicy(t *testing.T) {
	p := New(DefaultConfig(), ExactSampler{})
	if _, err := p.SelectSnapshot(snapshotRNG()); err != ErrNoArms {
		t.Fatalf("expected ErrNoArms, got %v", err)
	}
}

// Under concurrent Record/AddArm traffic every snapshot must be internally
// coherent: chosen in eligible, scores/posteriors keyed exactly by the
// eligible set, hash matching the snapshotted config. Run with -race.
func TestSelectSnapshotConcurrentCoherence(t *testing.T) {
	p := NewDefault("a", "b")
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: mutate posteriors continuously.
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := snapshotRNG()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				arm := "a"
				if i%2 == 1 {
					arm = "b"
				}
				_ = p.Record(rng, arm, 0.7)
				i++
			}
		}
	}()

	// Arm churn: add arms while snapshots are taken.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			select {
			case <-stop:
				return
			default:
				p.AddArm("churn")
				p.RemoveArm("churn")
			}
		}
	}()

	// Readers: validate coherence of every snapshot. A separate WaitGroup
	// tracks only the readers, so writer mutations overlap the whole phase.
	const readers = 8
	const iterations = 200
	errs := make(chan string, readers*iterations)
	var readersWg sync.WaitGroup
	readersWg.Add(readers)
	for r := 0; r < readers; r++ {
		go func(seed uint64) {
			defer readersWg.Done()
			rng := rand.New(rand.NewPCG(seed, seed>>1))
			for i := 0; i < iterations; i++ {
				snap, err := p.SelectSnapshot(rng)
				if err != nil {
					errs <- err.Error()
					return
				}
				if !slices.Contains(snap.Eligible, snap.Selected) {
					errs <- "selected not in eligible"
					return
				}
				if len(snap.Scores) != len(snap.Eligible) || len(snap.Posteriors) != len(snap.Eligible) {
					errs <- "score/posterior set size mismatch eligible set"
					return
				}
				for _, id := range snap.Eligible {
					if _, ok := snap.Scores[id]; !ok {
						errs <- "scores missing eligible arm"
						return
					}
					if _, ok := snap.Posteriors[id]; !ok {
						errs <- "posteriors missing eligible arm"
						return
					}
				}
				if snap.ConfigHash != hashConfig(snap.Config) {
					errs <- "config hash does not match snapshotted config"
					return
				}
			}
		}(uint64(r + 1))
	}

	// Writers stop only after every reader finished: mutations overlap the
	// entire read phase, then wg drains the writer loops.
	readersWg.Wait()
	close(stop)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("incoherent snapshot: %s", e)
	}
}
