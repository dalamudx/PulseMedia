package postgres_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

// Pagination identity is deterministic; separate tests cover real event/lease timing.
type automaticTVPagingClock struct {
	now    time.Time
	wall   bool
	offset time.Duration
}

func (c *automaticTVPagingClock) Now() time.Time {
	if c.wall {
		return time.Now().Add(c.offset)
	}
	return c.now
}
func (c *automaticTVPagingClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }
func (c *automaticTVPagingClock) advance(d time.Duration) {
	if c.wall {
		c.offset += d
	} else {
		c.now = c.now.Add(d)
	}
}

// Each task passes the real query proof, selector, reservations and preparation.
func automaticTVPagingSubmission(t *testing.T, f automaticTVFixture, ordinal int) automaticTVSubmissionFixture {
	t.Helper()
	name := "Trusted.Native.Series.2024.S02E03.mkv"
	raw := []byte(fmt.Sprintf("d4:infod6:lengthi1234e4:name%d:%s12:piece lengthi16384e6:pieces20:%020dee", len(name), name, ordinal))
	proof, err := ports.InspectAutomaticTVMetainfo(torrent.Inspector{}, raw)
	if err != nil {
		t.Fatal("paging metainfo", err)
	}
	round := f.start(t)
	candidate := tvExecutionCandidate(t, f, fmt.Sprintf("independent-%03d", ordinal), "1080p", 5)
	hash := proof.InfoHash()
	candidate.InfoHash = &hash
	tvExecutionPage(t, f, round, candidate)
	lease := tvExecutionLease(t, f, round)
	decision, err := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), round.ID, lease)
	if err != nil || decision.State != "reserved" {
		t.Fatal("independent finalization", ordinal, err)
	}
	job, err := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_download_submission"}, Column2: "paging-sender", Column3: 60000})
	if err != nil {
		t.Fatal("independent sender", err)
	}
	req := ports.AutomaticTVSendRequest{OperationID: decision.OperationID, Lease: ports.JobLease{ID: job.ID, Owner: "paging-sender", Token: job.LeaseToken}}
	codec := crypto.NewSecretProtector(bytes.Repeat([]byte{27}, 32))
	sf := automaticTVSubmissionFixture{automaticTVFixture: f, store: postgres.NewAutomaticTVSubmissionStore(f.pool, idgen.New(), codec), request: req, command: ports.PrepareAutomaticTVSubmissionCommand{Request: req, TaskID: uuid.New(), Metainfo: proof}}
	sf.prepare(t)
	if _, err = sf.store.RecordAutomaticTVSubmissionResult(t.Context(), sf.result(true, nil)); err != nil {
		t.Fatal("independent send result", err)
	}
	execIntent(t, f.pool, `UPDATE automatic_tv_physical_attempt SET finished_at=clock_timestamp() WHERE id=$1`, sf.command.PhysicalAttemptID)
	if _, err = sf.physical.Exec(t.Context(), `SELECT pg_advisory_unlock_all()`); err != nil {
		t.Fatal("release independent sender", err)
	}
	sf.physical.Release()
	sf.physical = nil
	return sf
}

func TestAutomaticTVWatchIndependentTargetPaging65PG(t *testing.T) {
	base := newAutomaticTVFixture(t)
	scan := uuid.New()
	execIntent(t, base.pool, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, base.tv)
	execIntent(t, base.pool, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,111,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, base.tv)
	senders := make(map[uuid.UUID]automaticTVSubmissionFixture)
	for i := 0; i < 65; i++ {
		tmdb := int64(551020 + i)
		season := creationSeason(2, 3)
		aired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		season.Episodes[0].AirDate = &aired
		season.Episodes[0].TMDBEpisodeID = tmdb*10 + 3
		metadata := creationTV(tmdb, season)
		metadata.Media.Title = "Trusted Native Series"
		year := int32(2024)
		metadata.Media.Year = &year
		in := creationInput(tmdb, "tv")
		in.IndexerIDs = []uuid.UUID{base.indexer}
		in.DownloaderID = &base.client
		created := base.create(t, in, metadata, fmt.Sprintf("independent-%03d", i))
		if len(created.Wanted) != 1 {
			t.Fatal("expected independent single episode")
		}
		f := base
		f.sub, f.unit, f.wanted = created.Subscription.ID, created.Wanted[0].UnitID, created.Wanted[0].ID
		sender := automaticTVPagingSubmission(t, f, i+1)
		senders[sender.request.OperationID] = sender
	}
	observer := postgres.NewAutomaticTVObservationStore(base.pool, idgen.New())
	if n, err := observer.ScheduleAutomaticTVObservations(t.Context(), 100); err != nil || n != 65 {
		t.Fatal("schedule first observations once", n, err)
	}
	expected := make(map[uuid.UUID]ports.AutomaticTVObservationRequest)
	var frozen *automaticTVWatchFixture
	for range 65 {
		job, err := db.New(base.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.AutomaticTVObservationJobKind}, Column2: "paging-observer", Column3: 60000})
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Operation uuid.UUID `json:"operation_id"`
			Task      uuid.UUID `json:"task_id"`
		}
		if err = json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		sender, ok := senders[payload.Operation]
		if !ok || sender.command.TaskID != payload.Task {
			t.Fatal("wrong independent observation")
		}
		obs := &automaticTVObservationFixture{automaticTVSubmissionFixture: sender, observer: observer, req: ports.AutomaticTVObservationRequest{OperationID: payload.Operation, TaskID: payload.Task, Lease: ports.JobLease{ID: job.ID, Owner: "paging-observer", Token: job.LeaseToken}}}
		obs.permit(t)
		snapshot := obs.load(t)
		result, err := observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, false))
		if err != nil || result.Complete || result.ReviewRequired {
			t.Fatal("trusted independent partial", err)
		}
		obs.finishPermit(t)
		expected[obs.req.TaskID] = obs.req
		frozen = &automaticTVWatchFixture{automaticTVObservationFixture: obs}
	}
	before := frozen.snapshot(t)
	clock := &automaticTVPagingClock{now: time.Now(), wall: os.Getenv("PULSE_M8_WATCH_WALL_CLOCK") == "1"}
	watch := postgres.NewAutomaticTVWatchStore(base.pool, idgen.New(), clock)
	sources, err := watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
	if err != nil || !sources.Complete || len(sources.Sources) != 1 || len(sources.Issues) != 0 {
		t.Fatal("one shared automatic TV source", err)
	}
	source := sources.Sources[0]
	clock.now = time.Now()
	// A close-write may match independent tasks with the same prepared path.
	hint := ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: "Trusted.Native.Series.2024.S02E03.mkv", Metadata: ports.RootChangeMetadata{EventID: uuid.New(), InstanceID: uuid.New(), Sequence: 1, ReceivedAt: clock.Now(), ManifestAt: source.ManifestAt, SourceIdentity: source.Binding.RootIdentity}}
	ignored := hint
	ignored.Kind = ports.RootChangeRescan
	if page, err := watch.ResolveDownloadWatchTargets(t.Context(), source.Binding, ignored, ports.DownloadWatchTargetQuery{}); err != nil || !page.Complete || len(page.Targets) != 0 {
		t.Fatal("rescan unexpectedly grants automatic TV targets", err)
	}
	seen := make(map[uuid.UUID]bool)
	cursor := ""
	for pageIndex := 0; pageIndex < 2; pageIndex++ {
		page, err := watch.ResolveDownloadWatchTargets(t.Context(), source.Binding, hint, ports.DownloadWatchTargetQuery{Cursor: cursor})
		want := 64
		if pageIndex == 1 {
			want = 1
		}
		if err != nil || len(page.Targets) != want || page.Complete != (pageIndex == 1) {
			t.Fatalf("page=%d targets=%d complete=%t elapsed_ms=%d error=%v", pageIndex, len(page.Targets), page.Complete, time.Since(hint.Metadata.ReceivedAt).Milliseconds(), err)
		}
		for _, target := range page.Targets {
			req, ok := expected[target.TaskID]
			if !ok || seen[target.TaskID] || target.OperationID != req.OperationID || target.Kind != "automatic_tv_observation_hint" || target.AutomaticTVHintCapture == nil {
				t.Fatal("wrong or duplicate independent task")
			}
			captured := target.AutomaticTVHintCapture.AutomaticTVObservationHintRequest()
			if captured.PriorJobID != req.Lease.ID || captured.TaskID != req.TaskID || captured.OperationID != req.OperationID || captured.SendAttemptID != senders[req.OperationID].command.PhysicalAttemptID || captured.EventID != hint.Metadata.EventID || captured.InstanceID != hint.Metadata.InstanceID || !captured.ReceivedAt.Equal(hint.Metadata.ReceivedAt) {
				t.Fatal("pagination rebound predecessor or event")
			}
			seen[target.TaskID] = true
		}
		cursor = page.NextCursor
		if (pageIndex == 0) == (cursor == "") {
			t.Fatal("invalid continuation boundary")
		}
	}
	elapsed := time.Since(hint.Metadata.ReceivedAt)
	clock.advance(5 * time.Second)
	if _, err := watch.ResolveDownloadWatchTargets(t.Context(), source.Binding, hint, ports.DownloadWatchTargetQuery{}); err != ports.ErrAutomaticTVObservationStale {
		t.Fatal("expired event accepted", err)
	}
	if len(seen) != 65 || before != frozen.snapshot(t) {
		t.Fatal("target omitted or target reads changed protected graph")
	}
	if clock.wall {
		t.Logf("M8_WATCH_TARGET_WALL_CLOCK_PASS family=automatic_tv targets=65 pages=64+1 elapsed_ms=%d read_only=true predecessor_unchanged=true expired_at_five_seconds_rejected=true", elapsed.Milliseconds())
		return
	}
	t.Log("M8_AUTOMATIC_TV_TARGET_PAGING_PASS targets=65 pages=64+1 independent_operations=true predecessor_frozen=true read_only=true two_second_gate_unchanged=true paging_clock=INJECTED expired_at_five_seconds_rejected=true wall_clock_65_performance=NOT_MEASURED")
}
