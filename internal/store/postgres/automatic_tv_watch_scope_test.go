package postgres_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestAutomaticTVWatchCursorIdentityPG(t *testing.T) {
	f := newAutomaticTVWatchFixture(t, true)
	raw, _ := json.Marshal(struct {
		Kind    string
		Binding ports.DownloadWatchBinding
		Hint    ports.RootChangeHint
	}{"automatic_tv_watch_targets_v1", f.source.Binding, f.hint})
	hash := sha256.Sum256(raw)
	raw, _ = json.Marshal(struct {
		Key   string `json:"key"`
		Sub   string `json:"sub"`
		Scope string `json:"scope"`
	}{uuid.Nil.String(), "", hex.EncodeToString(hash[:])})
	cursor := base64.RawURLEncoding.EncodeToString(raw)
	page, e := f.watch.ResolveDownloadWatchTargets(t.Context(), f.source.Binding, f.hint, ports.DownloadWatchTargetQuery{Cursor: cursor, Limit: 1})
	if e != nil || len(page.Targets) != 1 {
		t.Fatal(page, e)
	}
	for _, name := range []string{"event", "instance", "sequence", "received", "manifest", "identity", "kind", "path", "binding"} {
		t.Run(name, func(t *testing.T) {
			b, h := f.source.Binding, f.hint
			switch name {
			case "event":
				h.Metadata.EventID = uuid.New()
			case "instance":
				h.Metadata.InstanceID = uuid.New()
			case "sequence":
				h.Metadata.Sequence++
			case "received":
				h.Metadata.ReceivedAt = h.Metadata.ReceivedAt.Add(time.Nanosecond)
			case "manifest":
				h.Metadata.ManifestAt = h.Metadata.ManifestAt.Add(time.Microsecond)
			case "identity":
				h.Metadata.SourceIdentity.Inode++
			case "kind":
				h.Kind = ports.RootChangeMovedTo
			case "path":
				h.RelativePath = "other.mkv"
			case "binding":
				b.Stamp = "changed"
			}
			if _, e := f.watch.ResolveDownloadWatchTargets(t.Context(), b, h, ports.DownloadWatchTargetQuery{Cursor: cursor}); !errors.Is(e, ports.ErrRejected) {
				t.Fatal("cursor identity reused", e)
			}
		})
	}
	for _, scope := range []string{"sources", "complete_tv_watch_sources_v1"} {
		raw, _ := json.Marshal(struct {
			Key   string `json:"key"`
			Sub   string `json:"sub"`
			Scope string `json:"scope"`
		}{"", "", scope})
		if _, e := f.watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Cursor: base64.RawURLEncoding.EncodeToString(raw)}); !errors.Is(e, ports.ErrRejected) {
			t.Fatal("foreign source scope", e)
		}
	}
}

func TestAutomaticTVWatchManifestDoesNotRebasePG(t *testing.T) {
	f := newAutomaticTVWatchFixture(t, true)
	f.due(t)
	f.claim(t)
	snapshot := f.load(t)
	if _, e := f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, false)); e != nil {
		t.Fatal(e)
	}
	f.finishPermit(t)
	before := f.snapshot(t)
	page, e := f.watch.ResolveDownloadWatchTargets(t.Context(), f.source.Binding, f.hint, ports.DownloadWatchTargetQuery{})
	if e != nil || len(page.Targets) != 0 || !page.Complete {
		t.Fatal("old event rebound to later partial", page, e)
	}
	if _, e = f.watch.ScheduleAutomaticTVObservationHint(t.Context(), f.capture); e == nil {
		t.Fatal("old capture rebased")
	}
	if before != f.snapshot(t) {
		t.Fatal("old event wrote state")
	}
	sources, e := f.watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
	if e != nil || len(sources.Sources) != 1 {
		t.Fatal(sources, e)
	}
	h := f.hint
	h.Metadata.EventID = uuid.New()
	h.Metadata.Sequence++
	h.Metadata.ReceivedAt = time.Now()
	h.Metadata.ManifestAt = sources.Sources[0].ManifestAt
	page, e = f.watch.ResolveDownloadWatchTargets(t.Context(), f.source.Binding, h, ports.DownloadWatchTargetQuery{})
	if e != nil || len(page.Targets) != 1 {
		t.Fatal("new event should bind new partial", page, e)
	}
}

func TestAutomaticTVWatchSourceQualificationPG(t *testing.T) {
	cases := []struct{ name, sql string }{
		{"owner", "UPDATE app_user SET is_active=false"},
		{"subscription", "UPDATE subscription SET state='paused'"},
		{"source", "UPDATE indexer_lifecycle SET enabled=false"},
		{"client", "UPDATE download_client SET username='invalid'"},
		{"protection", "UPDATE acquisition_reservation SET released_at=clock_timestamp()"},
		{"baseline", "UPDATE scan_finalization SET expected_marker_filename='invalid'"},
		{"latest-failed", `INSERT INTO job(id,kind,state,payload) SELECT gen_random_uuid(),kind,'dead',payload FROM job WHERE kind='automatic_tv_observation'`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAutomaticTVWatchFixture(t, true)
			execIntent(t, f.pool, c.sql)
			before := f.snapshot(t)
			page, e := f.watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
			if e != nil || len(page.Sources) != 0 {
				t.Fatal("invalid source", page, e)
			}
			if c.name == "baseline" && (page.Complete || len(page.Issues) == 0) {
				t.Fatal("fake complete coverage")
			}
			if f.snapshot(t) != before {
				t.Fatal("source projection wrote state")
			}
		})
	}
}

func TestAutomaticTVWatchTimestampInstantPG(t *testing.T) {
	f := newAutomaticTVWatchFixture(t, true)
	r := f.capture.AutomaticTVObservationHintRequest()
	r.Binding.ClientVersion = r.Binding.ClientVersion.In(time.FixedZone("offset", 3600))
	r.ManifestAt = r.ManifestAt.In(time.FixedZone("offset", -7*3600))
	c, e := f.watch.ResolveAutomaticTVObservationHint(t.Context(), r)
	if e != nil {
		t.Fatal("same DB instant rejected", e)
	}
	f.due(t)
	out, e := f.watch.ScheduleAutomaticTVObservationHint(t.Context(), c)
	if e != nil || !out.Queued {
		t.Fatal(out, e)
	}
}

type automaticTVWatchLocalClock struct{ elapsed time.Duration }

func (c *automaticTVWatchLocalClock) Now() time.Time {
	return time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
}
func (c *automaticTVWatchLocalClock) Since(t time.Time) time.Duration {
	return time.Since(t) + c.elapsed
}
func TestAutomaticTVWatchLocalTTLPG(t *testing.T) {
	f := newAutomaticTVWatchFixture(t, true)
	clock := &automaticTVWatchLocalClock{}
	store := postgres.NewAutomaticTVWatchStore(f.pool, idgen.New(), clock)
	request := f.capture.AutomaticTVObservationHintRequest()
	capture, e := store.ResolveAutomaticTVObservationHint(t.Context(), request)
	if e != nil {
		t.Fatal("local wallclock compared with PG horizon", e)
	}
	before := f.snapshot(t)
	clock.elapsed = 6 * time.Second
	if _, e = store.ScheduleAutomaticTVObservationHint(t.Context(), capture); e == nil {
		t.Fatal("expired captured event")
	}
	if _, e = store.ResolveAutomaticTVObservationHint(t.Context(), request); e == nil {
		t.Fatal("expired event resolution")
	}
	clock.elapsed = -time.Hour
	if _, e = store.ScheduleAutomaticTVObservationHint(t.Context(), capture); e == nil {
		t.Fatal("future local event")
	}
	if before != f.snapshot(t) {
		t.Fatal("TTL rejection wrote state")
	}
}
