package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type movieWatchClock struct{ elapsed atomic.Int64 }

func (c *movieWatchClock) Now() time.Time { return time.Now() }
func (c *movieWatchClock) Since(t time.Time) time.Duration {
	return time.Since(t) + time.Duration(c.elapsed.Load())
}

func TestMovieUpgradeWatchTTLAndPhysicalSessionPG(t *testing.T) {
	f := newMovieUpgradeWatchFixture(t)
	clock := &movieWatchClock{}
	watch, e := postgres.NewMovieUpgradeWatchStore(f.runtime, clock)
	if e != nil {
		t.Fatal(e)
	}
	c, e := watch.ResolveMovieUpgradeTrackingHint(t.Context(), f.capture.MovieUpgradeTrackingHintRequest())
	if e != nil {
		t.Fatal(e)
	}
	before := f.snapshot(t)
	clock.elapsed.Store(int64(40 * time.Second))
	if e = watch.ValidateMovieUpgradeTrackingHint(t.Context(), c); !errors.Is(e, ports.ErrDownloadWatchStale) {
		t.Fatal("40s TTL", e)
	}
	if _, e = watch.ScheduleMovieUpgradeTrackingHint(t.Context(), c); !errors.Is(e, ports.ErrDownloadWatchStale) {
		t.Fatal("expired schedule", e)
	}
	conn, e := f.p.Acquire(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	var key int64
	if e = conn.QueryRow(t.Context(), `SELECT advisory_key FROM movie_upgrade_tracking_observation WHERE observation_id=$1`, f.capture.MovieUpgradeTrackingHintRequest().PriorObservationID).Scan(&key); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, key); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(t.Context()), `SELECT pg_advisory_unlock($1)`, key); err != nil {
			t.Error(err)
		}
	}()
	if e = f.watch.ValidateMovieUpgradeTrackingHint(t.Context(), f.capture); e == nil {
		t.Fatal("active physical session validated")
	}
	if _, e = f.watch.ScheduleMovieUpgradeTrackingHint(t.Context(), f.capture); e == nil {
		t.Fatal("active physical session scheduled")
	}
	sources, e := f.watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
	if e != nil || len(sources.Sources) != 0 {
		t.Fatal("active physical source", sources, e)
	}
	if f.snapshot(t) != before {
		t.Fatal("TTL/session validation wrote state")
	}
}

func TestMovieUpgradeWatchSourcesPagingPG(t *testing.T) {
	// One authorized task, library identity plus 64 complete mappings: 65 bindings.
	f := newMovieUpgradeWatchFixture(t, func(b *frozenUpgradeFixture) {
		mapping := map[string]string{}
		for i := range 64 {
			mapping[fmt.Sprintf("/watch-extra/%03d", i)] = fmt.Sprintf("/watch-local/%03d", i)
		}
		raw, e := json.Marshal(mapping)
		if e != nil {
			t.Fatal(e)
		}
		execIntent(t, b.p, `UPDATE download_client SET remote_path_map=$2 WHERE id=$1`, b.client, raw)
	})
	before := f.snapshot(t)
	page, e := f.watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
	if e != nil || len(page.Sources) != 64 || page.NextCursor == "" || page.Complete {
		t.Fatalf("first: %+v %v", page, e)
	}
	tail, e := f.watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Cursor: page.NextCursor})
	if e != nil || len(tail.Sources) != 1 || tail.NextCursor != "" || !tail.Complete {
		t.Fatalf("tail: %+v %v", tail, e)
	}
	seen := map[string]bool{}
	for _, s := range append(page.Sources, tail.Sources...) {
		if seen[s.Binding.Stamp] || s.ManifestAt.IsZero() {
			t.Fatal("duplicate source or absent DB horizon")
		}
		seen[s.Binding.Stamp] = true
	}
	if f.snapshot(t) != before {
		t.Fatal("paging wrote rows")
	}
}

func TestMovieUpgradeWatchCursorEnvelopePG(t *testing.T) {
	f := newMovieUpgradeWatchFixture(t)
	r := f.capture.MovieUpgradeTrackingHintRequest()
	r.OperationID = uuid.Nil
	r.TaskID = uuid.Nil
	r.PriorObservationID = uuid.Nil
	r.Binding.ClientVersion = r.Binding.ClientVersion.UTC()
	r.ReceivedAt = r.ReceivedAt.UTC()
	r.ManifestAt = r.ManifestAt.UTC()
	raw, e := json.Marshal(struct {
		Kind    string
		Request ports.MovieUpgradeTrackingHintRequest
	}{"movie_upgrade_watch_targets_v1", r})
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(raw)
	cursorRaw, e := json.Marshal(struct{ Key, Sub, Scope string }{"", "", hex.EncodeToString(sum[:])})
	if e != nil {
		t.Fatal(e)
	}
	// The wire format uses lowercase keys.
	var cursorFields map[string]string
	if e = json.Unmarshal(cursorRaw, &cursorFields); e != nil {
		t.Fatal(e)
	}
	cursorRaw, e = json.Marshal(struct {
		Key   string `json:"key"`
		Sub   string `json:"sub"`
		Scope string `json:"scope"`
	}{"", "", cursorFields["Scope"]})
	if e != nil {
		t.Fatal(e)
	}
	cursor := base64.RawURLEncoding.EncodeToString(cursorRaw)
	page, e := f.watch.ResolveDownloadWatchTargets(t.Context(), f.source.Binding, f.hint, ports.DownloadWatchTargetQuery{Cursor: cursor})
	if e != nil || len(page.Targets) != 1 {
		t.Fatal("original envelope", page, e)
	}
	for _, field := range []string{"event", "instance", "sequence", "received", "manifest", "source", "kind", "path", "binding"} {
		t.Run(field, func(t *testing.T) {
			h := f.hint
			b := f.source.Binding
			switch field {
			case "event":
				h.Metadata.EventID = uuid.New()
			case "instance":
				h.Metadata.InstanceID = uuid.New()
			case "sequence":
				h.Metadata.Sequence++
			case "received":
				h.Metadata.ReceivedAt = h.Metadata.ReceivedAt.Add(-time.Millisecond)
			case "manifest":
				h.Metadata.ManifestAt = h.Metadata.ManifestAt.Add(-time.Millisecond)
			case "source":
				h.Metadata.SourceIdentity.Inode++
			case "kind":
				h.Kind = ports.RootChangeMovedTo
			case "path":
				h.RelativePath = "sibling.mkv"
			case "binding":
				b.RootID = uuid.New()
			}
			if _, err := f.watch.ResolveDownloadWatchTargets(t.Context(), b, h, ports.DownloadWatchTargetQuery{Cursor: cursor}); err == nil {
				t.Fatal("cursor accepted another envelope")
			}
		})
	}
	h := f.hint
	h.Metadata.ManifestAt = h.Metadata.ManifestAt.Add(-time.Hour)
	page, e = f.watch.ResolveDownloadWatchTargets(t.Context(), f.source.Binding, h, ports.DownloadWatchTargetQuery{})
	if e != nil || len(page.Targets) != 0 {
		t.Fatal("old horizon selected newer predecessor", page, e)
	}
	// Equivalent instants in another zone keep the same cursor binding.
	h = f.hint
	h.Metadata.ReceivedAt = h.Metadata.ReceivedAt.In(time.FixedZone("watch", 3600))
	h.Metadata.ManifestAt = h.Metadata.ManifestAt.In(time.FixedZone("watch", 3600))
	page, e = f.watch.ResolveDownloadWatchTargets(t.Context(), f.source.Binding, h, ports.DownloadWatchTargetQuery{Cursor: cursor})
	if e != nil || len(page.Targets) != 1 {
		t.Fatal("equivalent times", page, e)
	}
}

func TestMovieUpgradeWatchNonPartialSourcesPG(t *testing.T) {
	for _, mode := range []string{"initial", "completed", "manual_review", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			var native *trackingFixture
			var jobs *trackingJobFixture
			if mode == "unknown" {
				native = nativeTrackingFixture(t, "ambiguous")
			} else {
				jobs = nativeTrackingJobFixture(t)
				native = jobs.trackingFixture
			}
			if v, e := postgres.Migrate(t.Context(), native.dsn, "../../../migrations"); e != nil || v != postgres.CurrentMigrationVersion {
				t.Fatal(v, e)
			}
			scan := uuid.New()
			execIntent(t, native.p, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, native.root)
			execIntent(t, native.p, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,111,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, native.root)
			runtime, e := postgres.NewMovieUpgradeTrackingRuntimeStore(native.preparation)
			if e != nil {
				t.Fatal(e)
			}
			watch, e := postgres.NewMovieUpgradeWatchStore(runtime, hintClock{})
			if e != nil {
				t.Fatal(e)
			}
			if mode == "completed" || mode == "manual_review" {
				if mode == "manual_review" {
					native.wrongHash.Store(true)
				}
				if n, e := runtime.ScheduleMovieUpgradeTracking(t.Context()); e != nil || n != 1 {
					t.Fatal(n, e)
				}
				req := jobs.claim(t, 2*time.Minute)
				out, e := jobs.jobRepo.TrackMovieUpgradeJob(t.Context(), native.actor, req)
				if e != nil || out == nil || out.Outcome != mode {
					t.Fatal(out, e)
				}
			}
			page, e := watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
			if e != nil || !page.Complete || len(page.Sources) != 0 {
				t.Fatalf("%s source: %+v %v", mode, page, e)
			}
		})
	}
}
