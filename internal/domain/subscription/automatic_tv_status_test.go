package subscription

import (
	"testing"
	"time"
)

func TestAutomaticTVStatusPreservesSafetyGates(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason string
		change               func(*Snapshot)
	}{
		{"aired standard episode", "missing", "missing", func(*Snapshot) {}},
		{"unknown date", "waiting", "air_date_unknown", func(s *Snapshot) { s.Units[0].AirDate = nil }},
		{"future date", "waiting", "future_air_date", func(s *Snapshot) { date := s.ObservedAt.Add(time.Hour); s.Units[0].AirDate = &date }},
		{"retired", "waiting", "unit_retired", func(s *Snapshot) { s.Units[0].Retired = true }},
		{"paused", "waiting", "subscription_paused", func(s *Snapshot) { s.State = "paused" }},
		{"no sources", "waiting", "no_indexers", func(s *Snapshot) { s.BoundIndexers = 0 }},
		{"source backoff", "waiting", "endpoint_backoff", func(s *Snapshot) { s.EndpointsReady = false }},
		{"no downloader", "waiting", "no_downloader", func(s *Snapshot) { s.DownloaderAvailable = false }},
		{"unresolved send", "in_flight", "reservation_unknown", func(s *Snapshot) { s.Units[0].Reserved = true; s.Units[0].ReservationUnknown = true }},
		{"unsupported media", "waiting", "automation_unavailable", func(s *Snapshot) { s.MediaType = "audio" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := readySnapshot()
			s.MediaType = "tv"
			s.Units[0].Key = "S01E03"
			aired := s.ObservedAt.Add(-time.Hour)
			s.Units[0].AirDate = &aired
			tc.change(&s)
			r := DeriveStatus(s)
			if len(r.Items) != 1 {
				t.Fatalf("unexpected items: %+v", r)
			}
			item := r.Items[0]
			if item.Status != tc.status || item.WaitReason != tc.reason || item.IsSatisfied || item.CurrentFactStage != "" {
				t.Fatalf("unexpected TV status: %+v", item)
			}
			if s.MediaType == "tv" {
				for _, blocker := range r.Blockers {
					if blocker == "automation_unavailable" {
						t.Fatal("enabled TV support reported unavailable")
					}
				}
			}
		})
	}
}
