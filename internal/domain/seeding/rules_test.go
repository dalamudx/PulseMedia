package seeding_test

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"math"
	"testing"
)

func number[T int64 | float64](n T) *T { return &n }
func protection() seeding.Protection   { return seeding.Protection{Owned: true, Imported: true} }
func TestSeedingNativeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		obs  seeding.Observation
		rule seeding.Rule
		want bool
	}{
		{"seconds-equal", seeding.Observation{Complete: true, SeedingSeconds: number(int64(3600))}, seeding.Rule{Mode: seeding.MatchAny, MinSeedingSeconds: number(int64(3600))}, true},
		{"seconds-below", seeding.Observation{Complete: true, SeedingSeconds: number(int64(3599))}, seeding.Rule{Mode: seeding.MatchAny, MinSeedingSeconds: number(int64(3600))}, false},
		{"ratio-equal", seeding.Observation{Complete: true, ShareRatio: number(2.0)}, seeding.Rule{Mode: seeding.MatchAny, MinShareRatio: number(2.0)}, true},
		{"ratio-below", seeding.Observation{Complete: true, ShareRatio: number(1.9999)}, seeding.Rule{Mode: seeding.MatchAny, MinShareRatio: number(2.0)}, false},
		{"native-infinite", seeding.Observation{Complete: true, InfiniteShareRatio: true}, seeding.Rule{Mode: seeding.MatchAny, MinShareRatio: number(2.0)}, true},
		{"known-zero-space", seeding.Observation{Complete: true, FreeBytes: number(int64(0)), FreeSpaceMatchesDownloadPath: true}, seeding.Rule{Mode: seeding.MatchAny, LowFreeBytes: number(int64(10))}, true},
		{"space-equal-not-low", seeding.Observation{Complete: true, FreeBytes: number(int64(10)), FreeSpaceMatchesDownloadPath: true}, seeding.Rule{Mode: seeding.MatchAny, LowFreeBytes: number(int64(10))}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := seeding.Evaluate(&tc.obs, protection(), tc.rule)
			if err != nil || got.Eligible != tc.want {
				t.Fatal(got, err)
			}
		})
	}
}
func TestSeedingProtectionAndUnknown(t *testing.T) {
	r := seeding.Rule{Mode: seeding.MatchAny, MinShareRatio: number(2.0)}
	ready := seeding.Observation{Complete: true, ShareRatio: number(3.0)}
	for _, tc := range []struct {
		name   string
		o      *seeding.Observation
		p      seeding.Protection
		reason string
	}{{"unowned", &ready, seeding.Protection{}, "not_owned"}, {"not-imported", &ready, seeding.Protection{Owned: true}, "not_imported"}, {"pending-work", &ready, seeding.Protection{Owned: true, Imported: true, PendingWork: true}, "pending_work"}, {"unknown", nil, protection(), "unknown_observation"}, {"incomplete", &seeding.Observation{ShareRatio: number(3.0)}, protection(), "incomplete"}, {"unknown-ratio", &seeding.Observation{Complete: true}, protection(), "unknown_facts"}} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := seeding.Evaluate(tc.o, tc.p, r)
			if err != nil || v.Eligible || v.Reason != tc.reason {
				t.Fatal(v, err)
			}
		})
	}
}
func TestSeedingAllAnyAndSpaceBinding(t *testing.T) {
	r := seeding.Rule{Mode: seeding.MatchAll, MinSeedingSeconds: number(int64(3600)), MinShareRatio: number(2.0), LowFreeBytes: number(int64(100))}
	o := seeding.Observation{Complete: true, SeedingSeconds: number(int64(3600)), ShareRatio: number(2.0), FreeBytes: number(int64(1))}
	v, err := seeding.Evaluate(&o, protection(), r)
	if err != nil || v.Eligible || v.Reason != "unknown_facts" {
		t.Fatal("wrong filesystem used", v, err)
	}
	o.FreeSpaceMatchesDownloadPath = true
	v, err = seeding.Evaluate(&o, protection(), r)
	if err != nil || !v.Eligible {
		t.Fatal(v, err)
	}
	o.ShareRatio = nil
	v, _ = seeding.Evaluate(&o, protection(), r)
	if v.Eligible {
		t.Fatal("unknown met all")
	}
	r.Mode = seeding.MatchAny
	v, _ = seeding.Evaluate(&o, protection(), r)
	if !v.Eligible {
		t.Fatal("known time match lost")
	}
}
func TestSeedingInvalidPolicy(t *testing.T) {
	for _, r := range []seeding.Rule{{}, {Mode: "guess", MinShareRatio: number(2.0)}, {Mode: seeding.MatchAny, DeleteFiles: true, MinShareRatio: number(2.0)}, {Mode: seeding.MatchAny, MinShareRatio: number(math.NaN())}, {Mode: seeding.MatchAny, MinShareRatio: number(math.Inf(1))}, {Mode: seeding.MatchAny, MinShareRatio: number(-1.0)}, {Mode: seeding.MatchAny, MinSeedingSeconds: number(int64(0))}, {Mode: seeding.MatchAny, MinSeedingSeconds: number(int64(315360001))}, {Mode: seeding.MatchAny, LowFreeBytes: number(int64(0))}} {
		if !errors.Is(r.Validate(), seeding.ErrInvalidRule) {
			t.Fatal("unsafe rule accepted")
		}
	}
}
func TestSeedingInvalidNativeAndNoMutation(t *testing.T) {
	r := seeding.Rule{Mode: seeding.MatchAny, MinShareRatio: number(2.0)}
	for _, o := range []seeding.Observation{{Complete: true, ShareRatio: number(math.NaN())}, {Complete: true, ShareRatio: number(math.Inf(1))}, {Complete: true, ShareRatio: number(-2.0)}, {Complete: true, ShareRatio: number(2.0), InfiniteShareRatio: true}, {Complete: true, SeedingSeconds: number(int64(-1))}, {Complete: true, FreeBytes: number(int64(-1))}} {
		if _, err := seeding.Evaluate(&o, protection(), r); !errors.Is(err, seeding.ErrInvalidObservation) {
			t.Fatal(err)
		}
	}
	ratio := 2.0
	o := seeding.Observation{Complete: true, ShareRatio: &ratio}
	if _, err := seeding.Evaluate(&o, protection(), r); err != nil || ratio != 2 || *r.MinShareRatio != 2 {
		t.Fatal("mutation", err)
	}
}
