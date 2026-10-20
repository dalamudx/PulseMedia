package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type siteCheckinTestClock struct{}

func (siteCheckinTestClock) Now() time.Time { return time.Unix(1700000000, 0) }

func siteCheckinBuilderSnapshot() ports.SiteCheckinExecution {
	now := siteCheckinTestClock{}.Now()
	return ports.SiteCheckinExecution{
		DayID: uuid.New(), ExecutionID: uuid.New(), CalendarDay: "2026-10-20",
		Config:    ports.SiteCheckinConfig{IndexerID: uuid.New(), ExpectedAccountID: "42", Profile: ports.NexusPHP1103CheckinProfile, Enabled: true, Timezone: "Asia/Shanghai", DailyAt: "09:00", Version: 1, SourceConfigVersion: 1, CreatedAt: now, UpdatedAt: now},
		SourceURL: "https://snapshot.example/pt/", Cookie: "session=snapshot",
	}
}

func TestSiteCheckinBuilderValidation(t *testing.T) {
	builder := &siteCheckinClientBuilder{clock: siteCheckinTestClock{}}
	cases := map[string]func(*ports.SiteCheckinExecution){
		"profile":        func(s *ports.SiteCheckinExecution) { s.Config.Profile = ports.NexusPHPClassicAccountStatsProfile },
		"uid":            func(s *ports.SiteCheckinExecution) { s.Config.ExpectedAccountID = "042" },
		"uid_overflow":   func(s *ports.SiteCheckinExecution) { s.Config.ExpectedAccountID = "18446744073709551616" },
		"day":            func(s *ports.SiteCheckinExecution) { s.CalendarDay = "2026-02-30" },
		"day_id":         func(s *ports.SiteCheckinExecution) { s.DayID = uuid.Nil },
		"execution_id":   func(s *ports.SiteCheckinExecution) { s.ExecutionID = uuid.Nil },
		"source_id":      func(s *ports.SiteCheckinExecution) { s.Config.IndexerID = uuid.Nil },
		"disabled":       func(s *ports.SiteCheckinExecution) { s.Config.Enabled = false },
		"version":        func(s *ports.SiteCheckinExecution) { s.Config.Version = 0 },
		"source_version": func(s *ports.SiteCheckinExecution) { s.Config.SourceConfigVersion = 0 },
		"created":        func(s *ports.SiteCheckinExecution) { s.Config.CreatedAt = time.Time{} },
		"updated":        func(s *ports.SiteCheckinExecution) { s.Config.UpdatedAt = s.Config.CreatedAt.Add(-time.Second) },
		"timezone":       func(s *ports.SiteCheckinExecution) { s.Config.Timezone = "Local" },
		"daily_at":       func(s *ports.SiteCheckinExecution) { s.Config.DailyAt = "24:00" },
		"source_url":     func(s *ports.SiteCheckinExecution) { s.SourceURL = " " },
		"cookie":         func(s *ports.SiteCheckinExecution) { s.Cookie = " " },
		"cookie_header":  func(s *ports.SiteCheckinExecution) { s.Cookie = "session=x\r\nHeader: x" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := siteCheckinBuilderSnapshot()
			mutate(&snapshot)
			client, err := builder.NewSiteCheckinClient(snapshot)
			if client != nil || !errors.Is(err, ports.ErrRejected) {
				t.Fatalf("invalid snapshot accepted: %v", err)
			}
		})
	}
	snapshot := siteCheckinBuilderSnapshot()
	snapshot.Config.ExpectedAccountID = "18446744073709551615"
	if _, err := builder.NewSiteCheckinClient(snapshot); err != nil {
		t.Fatalf("canonical maximum UID rejected: %v", err)
	}
	if client, err := (&siteCheckinClientBuilder{}).NewSiteCheckinClient(snapshot); client != nil || !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("missing clock accepted: %v", err)
	}
}

func TestSiteCheckinBuilderSnapshotNativeMethods(t *testing.T) {
	for _, observe := range []bool{true, false} {
		name := "submit"
		if observe {
			name = "observe"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				method, path := http.MethodPost, "/pt/attendance.php"
				if observe {
					method, path = http.MethodGet, "/pt/index.php"
				}
				if r.Method != method || r.URL.Path != path || r.Header.Get("Cookie") != "session=snapshot" {
					t.Error("native request did not preserve snapshot or method")
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			builder := &siteCheckinClientBuilder{httpClient: server.Client(), clock: siteCheckinTestClock{}}
			snapshot := siteCheckinBuilderSnapshot()
			snapshot.SourceURL = server.URL + "/pt/"
			snapshot.ObservationOnly = observe
			native, err := builder.NewSiteCheckinClient(snapshot)
			if err != nil || calls.Load() != 0 {
				t.Fatalf("constructor performed I/O or failed: %v", err)
			}
			other, err := builder.NewSiteCheckinClient(snapshot)
			if err != nil || native == other {
				t.Fatal("factory must return separate native clients")
			}
			snapshot.SourceURL = "https://changed.example/"
			snapshot.Cookie = "session=changed"
			if observe {
				_, err = native.ObserveCheckin(context.Background(), "42")
			} else {
				var result ports.CheckinSubmission
				result, err = native.SubmitCheckin(context.Background(), "42")
				if result.State != ports.CheckinSubmissionUnknown {
					t.Error("dispatched POST must retain uncertainty")
				}
			}
			if !errors.Is(err, ports.ErrAuthRequired) || calls.Load() != 1 {
				t.Fatalf("expected exactly one native request: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}
func (c siteCheckinTestClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }
