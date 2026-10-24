package postgres_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func tvWatch(t *testing.T) (*tvHintFixture, *postgres.CompleteTVWatchStore, ports.DownloadWatchSource, ports.RootChangeHint) {
	t.Helper()
	f := tvHint(t)
	s := postgres.NewCompleteTVWatchStore(f.p, idgen.New(), hintClock{})
	page, e := s.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
	if e != nil || !page.Complete || len(page.Sources) != 1 || len(page.Issues) != 0 {
		t.Fatalf("sources: %+v %v", page, e)
	}
	source := page.Sources[0]
	if source.Binding != f.r.Binding || source.ManifestAt.IsZero() {
		t.Fatalf("source baseline/horizon: %+v", source)
	}
	h := f.r.Hint
	h.Metadata = ports.RootChangeMetadata{EventID: f.r.HintID, InstanceID: f.r.InstanceID, Sequence: 1, ReceivedAt: time.Now(), ManifestAt: source.ManifestAt, SourceIdentity: f.r.SourceIdentity}
	return f, s, source, h
}

func tvWatchSnapshot(t *testing.T, f *tvHintFixture) string {
	t.Helper()
	return publisherFrozen(t, f.p, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_task x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM library_root x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_requirement x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM acquisition_reservation x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_reservation x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY job_id) FROM complete_tv_tracking_hint x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM complete_tv_physical_permit x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM complete_tv_execution_authority x),
 (SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM complete_tv_submission_preparation x))::text`)
}

func TestCompleteTVWatchReadCaptureSchedulePG(t *testing.T) {
	f, s, source, h := tvWatch(t)
	before := tvWatchSnapshot(t, f)
	for _, kind := range []ports.RootChangeKind{ports.RootChangeRescan, "", "create"} {
		page, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, ports.RootChangeHint{Kind: kind, RelativePath: "."}, ports.DownloadWatchTargetQuery{})
		if e != nil || !page.Complete || len(page.Targets) != 0 {
			t.Fatal(page, e)
		}
	}
	page, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, h, ports.DownloadWatchTargetQuery{})
	if e != nil || !page.Complete || len(page.Targets) != 1 {
		t.Fatal(page, e)
	}
	target := page.Targets[0]
	if target.Kind != "complete_tv_tracking_hint" || target.Binding != source.Binding || target.ClientVersion != source.Binding.ClientVersion || target.ClientStamp != source.Binding.ClientStamp || target.TaskID != f.r.TaskID || target.OperationID != f.r.OperationID {
		t.Fatalf("target: %+v", target)
	}
	request := target.TrackingHintCapture.TrackingHintRequest()
	if request.PriorJobID != f.r.PriorJobID || request.HintID != h.Metadata.EventID || request.Hint.Metadata != (ports.RootChangeMetadata{}) {
		t.Fatal("hint changed or metadata not sanitized")
	}
	replay, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, h, ports.DownloadWatchTargetQuery{})
	if e != nil || len(replay.Targets) != 1 || replay.Targets[0].TrackingHintCapture.TrackingHintRequest() != request {
		t.Fatal(replay, e)
	}
	if _, e = s.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{}); e != nil {
		t.Fatal(e)
	}
	if tvWatchSnapshot(t, f) != before {
		t.Fatal("read projection/capture mutated persistent state")
	}
	receipt, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), target.TrackingHintCapture)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.s.ScheduleCompleteTVTrackingHint(t.Context(), replay.Targets[0].TrackingHintCapture)
	if e != nil || *again != *receipt {
		t.Fatal(again, e)
	}
	if a, b := f.counts(t); a != 1 || b != 1 {
		t.Fatal(a, b)
	}
	var early bool
	if e = f.p.QueryRow(t.Context(), `SELECT h.accepted_at<j.updated_at+interval '30 seconds' FROM complete_tv_tracking_hint h JOIN job j ON j.id=h.prior_job_id`).Scan(&early); e != nil || !early {
		t.Fatal("not early", e)
	}
	page, e = s.ResolveDownloadWatchTargets(t.Context(), source.Binding, h, ports.DownloadWatchTargetQuery{})
	if e != nil || len(page.Targets) != 0 {
		t.Fatal("replay rebased to new job", page, e)
	}
}

func TestCompleteTVWatchMetadataPG(t *testing.T) {
	f, s, source, h := tvWatch(t)
	before := tvWatchSnapshot(t, f)
	for _, kind := range []string{"missing", "event", "instance", "sequence", "received", "manifest", "inode", "device", "future", "path", "expired", "horizon", "unrelated", "moved"} {
		t.Run(kind, func(t *testing.T) {
			changed := h
			changed.Metadata.ReceivedAt = time.Now()
			wantErr := true
			wantTargets := 0
			switch kind {
			case "missing":
				changed.Metadata = ports.RootChangeMetadata{}
			case "event":
				changed.Metadata.EventID = uuid.Nil
			case "instance":
				changed.Metadata.InstanceID = uuid.Nil
			case "sequence":
				changed.Metadata.Sequence = 0
			case "received":
				changed.Metadata.ReceivedAt = time.Time{}
			case "manifest":
				changed.Metadata.ManifestAt = time.Time{}
			case "inode":
				changed.Metadata.SourceIdentity.Inode = 222
			case "device":
				changed.Metadata.SourceIdentity.Device++
			case "future":
				changed.Metadata.ReceivedAt = time.Now().Add(time.Hour)
			case "path":
				changed.RelativePath = "../outside"
			case "expired":
				changed.Metadata.ReceivedAt = time.Now().Add(-time.Minute)
				wantErr = false
			case "horizon":
				changed.Metadata.ManifestAt = time.Unix(1, 0)
				wantErr = false
			case "unrelated":
				changed.RelativePath = "Other/Other.mkv"
				wantErr = false
			case "moved":
				changed.Kind = ports.RootChangeMovedTo
				wantErr = false
				wantTargets = 1
			}
			page, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, changed, ports.DownloadWatchTargetQuery{})
			if wantErr {
				if !errors.Is(e, ports.ErrRejected) {
					t.Fatalf("wanted rejection: %+v %v", page, e)
				}
			} else if e != nil || !page.Complete || len(page.Targets) != wantTargets {
				t.Fatal(page, e)
			}
		})
	}
	if tvWatchSnapshot(t, f) != before {
		t.Fatal("invalid hints mutated state")
	}
}

func TestCompleteTVWatchBindingDriftPG(t *testing.T) {
	for _, kind := range []string{"stamp", "root", "generation", "physical", "source", "mapping", "client"} {
		t.Run(kind, func(t *testing.T) {
			f, s, source, h := tvWatch(t)
			b := source.Binding
			switch kind {
			case "stamp":
				b.Stamp = "changed"
			case "root":
				b.RootID = uuid.New()
			case "generation":
				b.Generation++
			case "physical":
				b.RootIdentity.Device++
			case "source":
				b.LocalRoot = "/outside"
			case "mapping":
				execIntent(t, f.p, `UPDATE download_client SET remote_path_map='{"/remote":"/outside"}' WHERE id=$1`, b.ClientID)
			case "client":
				execIntent(t, f.p, `UPDATE download_client SET username='changed' WHERE id=$1`, b.ClientID)
			}
			_, e := s.ResolveDownloadWatchTargets(t.Context(), b, h, ports.DownloadWatchTargetQuery{})
			if !errors.Is(e, ports.ErrDownloadWatchStale) {
				t.Fatal(e)
			}
		})
	}
}

func TestCompleteTVWatchPredecessorPG(t *testing.T) {
	for _, kind := range []string{"failed", "completed", "review", "later-failed", "later-partial", "released", "savepath"} {
		t.Run(kind, func(t *testing.T) {
			f, s, source, h := tvWatch(t)
			switch kind {
			case "failed":
				execIntent(t, f.p, `UPDATE job SET state='dead' WHERE id=$1`, f.r.PriorJobID)
			case "completed":
				execIntent(t, f.p, `UPDATE job SET result=jsonb_set(result,'{state}','"completed"') WHERE id=$1`, f.r.PriorJobID)
			case "review":
				execIntent(t, f.p, `UPDATE job SET result=jsonb_set(result,'{review_required}','true') WHERE id=$1`, f.r.PriorJobID)
			case "later-failed":
				execIntent(t, f.p, `INSERT INTO job(id,kind,state,payload) SELECT $1,kind,'dead',payload FROM job WHERE id=$2`, uuid.New(), f.r.PriorJobID)
			case "later-partial":
				execIntent(t, f.p, `INSERT INTO job(id,kind,state,payload,result) SELECT $1,kind,state,payload,result FROM job WHERE id=$2`, uuid.New(), f.r.PriorJobID)
			case "released":
				execIntent(t, f.p, `UPDATE acquisition_reservation SET released_at=clock_timestamp() WHERE operation_id=$1`, f.r.OperationID)
			case "savepath":
				execIntent(t, f.p, `UPDATE download_task SET save_path='/outside' WHERE id=$1`, f.r.TaskID)
			}
			h.Metadata.ReceivedAt = time.Now()
			before := tvWatchSnapshot(t, f)
			page, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, h, ports.DownloadWatchTargetQuery{})
			if e != nil || !page.Complete || len(page.Targets) != 0 {
				t.Fatal(page, e)
			}
			if tvWatchSnapshot(t, f) != before {
				t.Fatal("stale candidate mutated state")
			}
		})
	}
}

func TestCompleteTVWatchCursorPG(t *testing.T) {
	_, s, source, h := tvWatch(t)
	cursor := func(scope, key, sub string) string {
		raw, _ := json.Marshal(struct {
			Key   string `json:"key"`
			Sub   string `json:"sub"`
			Scope string `json:"scope"`
		}{key, sub, scope})
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	for _, limit := range []int{-1, 257} {
		if _, e := s.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Limit: limit}); !errors.Is(e, ports.ErrRejected) {
			t.Fatal(e)
		}
		if _, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, h, ports.DownloadWatchTargetQuery{Limit: limit}); !errors.Is(e, ports.ErrRejected) {
			t.Fatal(e)
		}
	}
	for _, raw := range []string{"!", cursor("sources", "", ""), cursor("complete_tv_watch_sources_v1", "BAD", ""), cursor("complete_tv_watch_sources_v1", "", "sub")} {
		if _, e := s.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Cursor: raw}); !errors.Is(e, ports.ErrRejected) {
			t.Fatal(e)
		}
	}
	if _, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, h, ports.DownloadWatchTargetQuery{Cursor: cursor("sources", "", "")}); !errors.Is(e, ports.ErrRejected) {
		t.Fatal(e)
	}
	page, e := s.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Limit: 1})
	if e != nil || len(page.Sources) != 1 || !page.Complete {
		t.Fatal(page, e)
	}
	key := source.Binding.ClientID.String() + ":" + source.Binding.RootID.String()
	page, e = s.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{Limit: 1, Cursor: cursor("complete_tv_watch_sources_v1", key, "library_identity:")})
	if e != nil || len(page.Sources) != 0 || !page.Complete {
		t.Fatal(page, e)
	}
}

func TestCompleteTVWatchHealthStablePG(t *testing.T) {
	f, s, source, h := tvWatch(t)
	execIntent(t, f.p, `UPDATE library_root SET health_revision=health_revision+1 WHERE id=$1`, source.Binding.RootID)
	execIntent(t, f.p, `UPDATE download_task SET progress=0.6,updated_at=clock_timestamp() WHERE id=$1`, f.r.TaskID)
	page, e := s.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
	if e != nil || len(page.Sources) != 1 || page.Sources[0].Binding != source.Binding {
		t.Fatal(page, e)
	}
	h.Metadata.ReceivedAt = time.Now()
	targets, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, h, ports.DownloadWatchTargetQuery{})
	if e != nil || len(targets.Targets) != 1 {
		t.Fatal(targets, e)
	}
}
