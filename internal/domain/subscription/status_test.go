package subscription

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
	"time"
)

func readySnapshot() Snapshot {
	p := quality.DefaultPolicy()
	return Snapshot{State: "active", MediaType: "movie", RootHealth: "online", BaselineCompleted: true, PolicyAvailable: true, Rules: quality.PolicyRules{ResolutionOrder: p.ResolutionOrder, SourceOrder: p.SourceOrder}, BoundIndexers: 1, AvailableIndexers: 1, EndpointsReady: true, DownloaderAvailable: true, IntentVersion: 3, PolicyRevision: 2, ObservedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), Units: []UnitInput{{Key: "movie"}}}
}
func TestDeriveStatus(t *testing.T) {
	tests := []struct {
		name, status, reason string
		change               func(*Snapshot)
	}{
		{"missing", "missing", "missing", func(s *Snapshot) {}},
		{"offline", "unknown", "root_offline", func(s *Snapshot) { s.RootHealth = "offline" }},
		{"baseline", "unknown", "baseline_pending", func(s *Snapshot) { s.BaselineCompleted = false }},
		{"policy unavailable", "unknown", "policy_unavailable", func(s *Snapshot) { s.PolicyAvailable = false }},
		{"paused", "waiting", "subscription_paused", func(s *Snapshot) { s.State = "paused" }},
		{"skipped", "skipped", "skipped", func(s *Snapshot) { s.Units[0].Skipped = true }},
		{"retired", "waiting", "unit_retired", func(s *Snapshot) { s.Units[0].Retired = true }},
		{"future", "waiting", "future_air_date", func(s *Snapshot) { date := s.ObservedAt.Add(time.Hour); s.Units[0].AirDate = &date }},
		{"no indexes", "waiting", "no_indexers", func(s *Snapshot) { s.BoundIndexers = 0 }},
		{"endpoint offline", "waiting", "endpoints_offline", func(s *Snapshot) { s.AvailableIndexers = 0 }},
		{"endpoint budget", "waiting", "endpoint_backoff", func(s *Snapshot) { s.EndpointsReady = false }},
		{"downloader absent", "waiting", "no_downloader", func(s *Snapshot) { s.DownloaderAvailable = false }},
		{"reservation has no fact", "in_flight", "reservation_active", func(s *Snapshot) { s.Units[0].Reserved = true }},
		{"unknown reservation", "in_flight", "reservation_unknown", func(s *Snapshot) { s.Units[0].Reserved = true; s.Units[0].ReservationUnknown = true }},
		{"unverified file", "unknown", "quality_unknown", func(s *Snapshot) {
			s.Units[0].Files = []FileEvidence{{Profile: quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray, ResolutionParserVersion: quality.ResolutionTokenParserVersion, SourceParserVersion: quality.SourceTokenParserVersion}}}
		}},
		{"unknown quality", "unknown", "quality_unknown", func(s *Snapshot) { s.Units[0].Files = []FileEvidence{{Verified: true}} }},
		{"allowed below cutoff", "satisfied", "", func(s *Snapshot) {
			s.Rules.CutoffScore = 99999
			s.Units[0].Files = []FileEvidence{{Verified: true, Profile: quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray, ResolutionParserVersion: quality.ResolutionTokenParserVersion, SourceParserVersion: quality.SourceTokenParserVersion}}}
		}},
		{"all file versions", "satisfied", "", func(s *Snapshot) {
			s.Units[0].Files = []FileEvidence{{Verified: true}, {Verified: true, Profile: quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray, ResolutionParserVersion: quality.ResolutionTokenParserVersion, SourceParserVersion: quality.SourceTokenParserVersion}}}
		}},
		{"reject group", "missing", "file_rejected", func(s *Snapshot) {
			s.Rules.RejectGroups = []string{"bad"}
			s.Units[0].Files = []FileEvidence{{Verified: true, Profile: quality.Profile{Resolution: quality.Res1080p, Source: quality.SourceBluray, ResolutionParserVersion: quality.ResolutionTokenParserVersion, SourceParserVersion: quality.SourceTokenParserVersion, Group: "bad"}}}
		}},
		{"old round", "missing", "missing", func(s *Snapshot) {
			s.Units[0].Round = &RoundEvidence{State: "running", IntentVersion: 1, PolicyRevision: 2, Deadline: s.ObservedAt.Add(time.Hour)}
		}},
		{"current round", "waiting", "search_round_pending", func(s *Snapshot) {
			s.Units[0].Round = &RoundEvidence{State: "running", IntentVersion: 3, PolicyRevision: 2, Deadline: s.ObservedAt.Add(time.Hour)}
		}},
		{"deadline", "waiting", "round_finalization_pending", func(s *Snapshot) {
			s.Units[0].Round = &RoundEvidence{State: "running", IntentVersion: 3, PolicyRevision: 2, Deadline: s.ObservedAt}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := readySnapshot()
			test.change(&s)
			r := DeriveStatus(s)
			v := r.Items[0]
			if v.Status != test.status || v.WaitReason != test.reason {
				t.Fatalf("got %+v", v)
			}
			if v.Status == "in_flight" && v.CurrentFactStage != "" {
				t.Fatal("reservation fabricated submitted stage")
			}
			if v.IsSatisfied != (v.Status == "satisfied") {
				t.Fatal("inconsistent satisfaction")
			}
		})
	}
}
