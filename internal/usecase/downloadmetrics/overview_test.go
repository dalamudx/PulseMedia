package downloadmetrics

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func overviewFixture() (*ports.DownloadGlobalRatesSnapshot, ports.DownloadGlobalRateSource) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	id := uuid.New()
	down, up := int64(math.MaxInt64), int64(0)
	sample := &ports.DownloadGlobalRateSample{JobID: uuid.New(), ClientID: id, ConfigStamp: strings.Repeat("a", 64), State: "succeeded", ObservedAt: &now, DownloadBytesPerSecond: &down, UploadBytesPerSecond: &up}
	source := ports.DownloadGlobalRateSource{ID: id, Name: "native source", ClientType: "qbittorrent", ConfigStamp: sample.ConfigStamp, Latest: sample, LastGood: sample}
	return &ports.DownloadGlobalRatesSnapshot{ObservedAt: now, Sources: []ports.DownloadGlobalRateSource{source}}, source
}
func expectRate(t *testing.T, value *string, want string) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("rate expected %s, got %v", want, value)
	}
}

func TestGlobalOverviewExactTotalsAndKnownZero(t *testing.T) {
	snapshot, source := overviewFixture()
	other := source
	other.ID = uuid.New()
	sample := *source.Latest
	sample.ClientID = other.ID
	sample.JobID = uuid.New()
	other.Latest = &sample
	other.LastGood = &sample
	snapshot.Sources = append(snapshot.Sources, other)
	out, err := projectOverview(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	expectRate(t, out.DownloadBytesPerSecond, "18446744073709551614")
	expectRate(t, out.UploadBytesPerSecond, "0")
	if out.DownloadKnownClients != 2 || out.UploadKnownClients != 2 || out.Clients[0].State != "observed" {
		t.Fatal("known sources not counted")
	}
	expectRate(t, out.Clients[0].DownloadBytesPerSecond, "9223372036854775807")
	if len(out.Series) != 61 || !out.Series[len(out.Series)-1].At.Equal(snapshot.ObservedAt) {
		t.Fatal("bounded history window missing current point")
	}
}

func TestGlobalOverviewUnknownDirectionsAndAvailability(t *testing.T) {
	for _, kind := range []string{"missing_direction", "missing_source", "stale", "boundary", "failed", "abandoned", "configuration_changed", "future", "disabled", "empty"} {
		t.Run(kind, func(t *testing.T) {
			snapshot, source := overviewFixture()
			enabled := true
			want := "observed"
			copySample := *source.Latest
			source.Latest = &copySample
			switch kind {
			case "missing_direction":
				source.Latest.UploadBytesPerSecond = nil
			case "missing_source":
				source.Latest = nil
				want = "unobserved"
			case "stale":
				when := snapshot.ObservedAt.Add(-46 * time.Second)
				source.Latest.ObservedAt = &when
				want = "stale"
			case "boundary":
				when := snapshot.ObservedAt.Add(-45 * time.Second)
				source.Latest.ObservedAt = &when
			case "failed":
				source.Latest.State = "failed"
				source.Latest.FailureCode = "auth_required"
				source.Latest.DownloadBytesPerSecond = nil
				source.Latest.UploadBytesPerSecond = nil
				want = "failed"
			case "abandoned":
				source.Latest.State = "abandoned"
				source.Latest.FinishedAt = source.Latest.ObservedAt
				source.Latest.ObservedAt = nil
				source.Latest.DownloadBytesPerSecond = nil
				source.Latest.UploadBytesPerSecond = nil
				source.Latest.FailureCode = "unavailable"
				want = "failed"
			case "configuration_changed":
				source.ConfigStamp = strings.Repeat("b", 64)
				want = "configuration_changed"
			case "future":
				when := snapshot.ObservedAt.Add(time.Second)
				source.Latest.ObservedAt = &when
				want = "unknown"
			case "disabled":
				enabled = false
				want = "disabled"
			case "empty":
				snapshot.Sources = nil
			}
			if kind != "empty" {
				snapshot.Sources = []ports.DownloadGlobalRateSource{source}
			}
			out, err := projectOverview(snapshot, enabled)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "empty" {
				expectRate(t, out.DownloadBytesPerSecond, "0")
				expectRate(t, out.UploadBytesPerSecond, "0")
				return
			}
			if out.Clients[0].State != want {
				t.Fatal("wrong source state", out.Clients[0].State, want)
			}
			if kind == "missing_direction" {
				expectRate(t, out.DownloadBytesPerSecond, "9223372036854775807")
				if out.UploadBytesPerSecond != nil || out.UploadKnownClients != 0 {
					t.Fatal("unknown upload became zero")
				}
				return
			}
			if kind == "boundary" {
				expectRate(t, out.DownloadBytesPerSecond, "9223372036854775807")
				return
			}
			if out.DownloadBytesPerSecond != nil || out.UploadBytesPerSecond != nil || out.DownloadKnownClients != 0 || out.UploadKnownClients != 0 {
				t.Fatal("unknown source produced global rates")
			}
			if kind == "configuration_changed" && out.Clients[0].LastGood != nil {
				t.Fatal("old configuration last-good leaked into current source")
			}
			if kind == "failed" && out.Clients[0].LastGood == nil {
				t.Fatal("failed sample erased valid historical value")
			}
			if kind == "disabled" && len(out.Series) != 0 {
				t.Fatal("disabled source drew a live curve")
			}
		})
	}
}

func TestGlobalOverviewHistoryUsesAsOfEvidenceAndGaps(t *testing.T) {
	snapshot, source := overviewFixture()
	now := snapshot.ObservedAt
	atGood := now.Add(-30 * time.Second)
	atFailure := now.Add(-20 * time.Second)
	down, up := int64(10), int64(20)
	good := ports.DownloadGlobalRateSample{JobID: uuid.New(), ClientID: source.ID, ConfigStamp: source.ConfigStamp, State: "succeeded", ObservedAt: &atGood, DownloadBytesPerSecond: &down, UploadBytesPerSecond: &up}
	failed := ports.DownloadGlobalRateSample{JobID: uuid.New(), ClientID: source.ID, ConfigStamp: source.ConfigStamp, State: "failed", ObservedAt: &atFailure, FailureCode: "timeout"}
	snapshot.History = []ports.DownloadGlobalRateSample{failed, good}
	savedFirst := snapshot.History[0].JobID
	out, err := projectOverview(snapshot, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, point := range out.Series {
		switch {
		case point.At.Before(atGood):
			if point.DownloadBytesPerSecond != nil {
				t.Fatal("future sample backfilled earlier history")
			}
		case point.At.Before(atFailure):
			expectRate(t, point.DownloadBytesPerSecond, "10")
		case point.At.Before(now):
			if point.DownloadBytesPerSecond != nil || point.UploadBytesPerSecond != nil {
				t.Fatal("failure gap was interpolated or changed to zero")
			}
		default:
			expectRate(t, point.DownloadBytesPerSecond, "9223372036854775807")
		}
	}
	if snapshot.History[0].JobID != savedFirst {
		t.Fatal("projection reordered caller-owned history")
	}
}

type overviewReaderStub struct {
	snapshot *ports.DownloadGlobalRatesSnapshot
	err      error
	calls    int
}

func (r *overviewReaderStub) ReadDownloadGlobalRatesSnapshot(context.Context, *ports.Actor) (*ports.DownloadGlobalRatesSnapshot, error) {
	r.calls++
	return r.snapshot, r.err
}
func TestGlobalOverviewAccessAndReadErrors(t *testing.T) {
	snapshot, _ := overviewFixture()
	admin := &ports.Actor{ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	for _, kind := range []string{"admin", "anonymous", "member", "token_scope", "invalid_id", "database", "revoked", "nil_snapshot", "duplicate_source"} {
		t.Run(kind, func(t *testing.T) {
			reader := &overviewReaderStub{snapshot: snapshot}
			actor := *admin
			input := &actor
			switch kind {
			case "anonymous":
				input = nil
			case "member":
				actor.Role = ports.RoleMember
			case "token_scope":
				actor.Scopes = []string{"read", "admin"}
			case "invalid_id":
				actor.ID = "invalid"
			case "database":
				reader.err = errors.New("private DSN")
			case "revoked":
				reader.err = ports.ErrForbidden
			case "nil_snapshot":
				reader.snapshot = nil
			case "duplicate_source":
				copySnapshot := *snapshot
				copySnapshot.Sources = append(append([]ports.DownloadGlobalRateSource{}, snapshot.Sources...), snapshot.Sources[0])
				reader.snapshot = &copySnapshot
			}
			out, err := NewOverviewService(reader, true).Read(t.Context(), input)
			switch kind {
			case "admin":
				if err != nil || out == nil || reader.calls != 1 {
					t.Fatal("admin read failed", err)
				}
			case "anonymous", "member", "token_scope", "invalid_id":
				if !errors.Is(err, ports.ErrForbidden) || reader.calls != 0 || out != nil {
					t.Fatal("unauthorized actor reached repository", err)
				}
			case "revoked":
				if !errors.Is(err, ports.ErrForbidden) {
					t.Fatal("revoked read misclassified", err)
				}
			default:
				if !errors.Is(err, ports.ErrUnavailable) || out != nil || strings.Contains(err.Error(), "private") {
					t.Fatal("invalid read exposed success or private error", err)
				}
			}
		})
	}
}
