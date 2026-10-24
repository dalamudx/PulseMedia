package postgres_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

// Build independent authority through review, reservations, preparation and observation.
func completeTVPagingTarget(t *testing.T, f creationFixture, indexer, client uuid.UUID, ordinal int) ports.CompleteTVTrackingRequest {
	t.Helper()
	key := fmt.Sprintf("target-%03d", ordinal)
	tmdb := int64(551020 + ordinal)
	in := creationInput(tmdb, "tv")
	in.IndexerIDs = []uuid.UUID{indexer}
	in.DownloaderID = &client
	season := creationSeason(1, 1, 2)
	aired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range season.Episodes {
		season.Episodes[i].AirDate = &aired
		season.Episodes[i].TMDBEpisodeID = tmdb*10 + int64(i+1)
	}
	metadata := creationTV(tmdb, season)
	metadata.Media.Title = "Show"
	year := int32(2020)
	metadata.Media.Year = &year
	created := f.create(t, in, metadata, key)
	raw := []byte(fmt.Sprintf("d4:infod5:filesld6:lengthi512e4:pathl15:Show.S01E01.mkveed6:lengthi512e4:pathl15:Show.S01E02.mkveee4:name4:Show12:piece lengthi1024e6:pieces20:%020dee", ordinal))
	proof, err := ports.InspectCompleteTVMetainfo(torrent.Inspector{}, raw)
	if err != nil {
		t.Fatal(err)
	}
	candidate, query := uuid.New(), uuid.New()
	profile, err := json.Marshal(quality.Profile{Resolution: "1080p", ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: "WEB-DL", SourceParserVersion: quality.SourceTokenParserVersion})
	if err != nil {
		t.Fatal(err)
	}
	execIntent(t, f.pool, `INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version,info_hash) SELECT $1,$2,$3,'Show.2020.S01E01E02.1080p.WEB-DL.mkv','fixture',1024,$4,config_version,$5 FROM indexer WHERE id=$2`, candidate, indexer, key, profile, proof.InfoHash())
	execIntent(t, f.pool, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count,finished_at) SELECT $1,$2,'search',$3,1,config_version,'succeeded',1,clock_timestamp() FROM indexer WHERE id=$2`, query, indexer, key)
	execIntent(t, f.pool, `INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)`, query, candidate)
	mapping := postgres.NewEpisodeMappingStore(postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(f.pool, idgen.New())), idgen.New())
	reviews := subscription.NewTVCoverageCandidateReviewService(postgres.NewTVCoverageCandidateReviewStore(mapping))
	preview, err := reviews.Preview(t.Context(), f.actor, created.Subscription.ID, candidate)
	if err != nil || !preview.CanReview {
		t.Fatal("independent preview", ordinal, err)
	}
	review, err := reviews.Review(t.Context(), f.actor, ports.TVCoverageCandidateReviewCommand{SubscriptionID: created.Subscription.ID, CandidateID: candidate, ExpectedInputStamp: preview.InputStamp, Confirmed: true, Reason: "Independent paging target"}, key+"-review")
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := acquisition.NewCompleteTVService(postgres.NewCompleteTVAcquisitionStore(f.pool, idgen.New())).Acquire(t.Context(), f.actor, ports.AcquireCompleteTVCoverageCommand{SubscriptionID: created.Subscription.ID, ReviewID: review.ID, ExpectedInputStamp: preview.InputStamp, ExpectedMaterialStamp: preview.MaterialStamp, Confirmed: true}, key+"-acquire")
	if err != nil {
		t.Fatal(err)
	}
	job, err := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"complete_tv_coverage_download_submission"}, Column2: "paging-sender", Column3: 60000})
	if err != nil || job.ID != reserved.JobID {
		t.Fatal("claim independent sender", ordinal, err)
	}
	taskID := uuid.New()
	_, err = postgres.NewCompleteTVSubmissionStore(f.pool, idgen.New()).PrepareCompleteTVSubmission(t.Context(), ports.PrepareCompleteTVSubmissionCommand{Lease: ports.JobLease{ID: job.ID, Owner: "paging-sender", Token: job.LeaseToken}, TaskID: taskID, OperationID: reserved.OperationID, Metainfo: proof})
	if err != nil {
		t.Fatal(err)
	}
	execIntent(t, f.pool, `UPDATE download_task SET state='tracking' WHERE id=$1`, taskID)
	req := ports.CompleteTVTrackingRequest{Lease: ports.JobLease{ID: uuid.New(), Owner: "paging-tracker", Token: 1}, OperationID: reserved.OperationID, TaskID: taskID}
	execIntent(t, f.pool, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) VALUES($1,$2,'running',jsonb_build_object('task_id',$3::text,'operation_id',$4::text),$5,$6,clock_timestamp()+interval '1 minute')`, req.Lease.ID, ports.CompleteTVTrackingJobKind, taskID.String(), req.OperationID.String(), req.Lease.Owner, req.Lease.Token)
	task, _ := rateTask(t, f.pool, req.OperationID)
	native := &publisherTrackingClient{snapshot: &ports.CompleteTVTrackingSnapshot{InfoHash: task.InfoHash, SavePath: task.SavePath, TotalBytes: 1024, OperationTag: "pulsemedia:op:" + req.OperationID.String()}, progress: 0.5}
	manager := download.NewManager(f.pool, idgen.New(), make([]byte, 32))
	manager.SetClientFactory(func(string, string, string, string) (ports.DownloadClient, error) { return native, nil })
	out, err := manager.ObserveCompleteTVTracking(t.Context(), postgres.NewCompleteTVTrackingStoreWithDownloadRates(f.pool, idgen.New()), torrent.Inspector{}, req)
	if err != nil || out.State != "tracking" || out.ReviewRequired {
		t.Fatal("trusted independent partial", ordinal, err)
	}
	execIntent(t, f.pool, `UPDATE job SET state='succeeded',lease_owner=NULL,lease_until=NULL WHERE id=$1`, job.ID)
	return req
}

func TestCompleteTVWatchIndependentTargetPaging65PG(t *testing.T) {
	f := newCreationFixture(t)
	execIntent(t, f.pool, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(f.actor.ID))
	f.actor.Role = ports.RoleAdmin
	indexer, client := uuid.New(), uuid.New()
	password, err := crypto.Encrypt(make([]byte, 32), []byte("paging-password"))
	if err != nil {
		t.Fatal(err)
	}
	execIntent(t, f.pool, `INSERT INTO indexer(id,name,url,api_key_encrypted,health) VALUES($1,'paging','https://paging.invalid','fixture','online')`, indexer)
	execIntent(t, f.pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'paging','qbittorrent','https://paging.invalid','fixture',$2,'{}')`, client, password)
	execIntent(t, f.pool, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1`, f.tv)
	scan := uuid.New()
	execIntent(t, f.pool, `INSERT INTO scan_run(id,root_id,scan_token,generation,start_mutation_seq,state,finished_at) SELECT $1,id,scan_token,generation,mutation_seq,'succeeded',clock_timestamp() FROM library_root WHERE id=$2`, scan, f.tv)
	execIntent(t, f.pool, `INSERT INTO scan_finalization(scan_run_id,root_id,expected_path,expected_media_type,expected_marker_filename,expected_health_revision,root_device,root_inode,exhausted,completed_at) SELECT $1,id,path,media_type,marker_filename,health_revision,1,111,true,clock_timestamp() FROM library_root WHERE id=$2`, scan, f.tv)
	expected := make(map[uuid.UUID]ports.CompleteTVTrackingRequest)
	for i := 0; i < 65; i++ {
		req := completeTVPagingTarget(t, f, indexer, client, i)
		expected[req.TaskID] = req
	}
	frozen := &tvHintFixture{p: f.pool}
	before := tvWatchSnapshot(t, frozen)
	watch := postgres.NewCompleteTVWatchStore(f.pool, idgen.New(), hintClock{})
	sources, err := watch.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
	if err != nil || !sources.Complete || len(sources.Sources) != 1 || len(sources.Issues) != 0 {
		t.Fatal("one shared source", err)
	}
	source := sources.Sources[0]
	hint := ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: "Show/Show.S01E01.mkv", Metadata: ports.RootChangeMetadata{EventID: uuid.New(), InstanceID: uuid.New(), Sequence: 1, ReceivedAt: time.Now(), ManifestAt: source.ManifestAt, SourceIdentity: source.Binding.RootIdentity}}
	seen := make(map[uuid.UUID]bool)
	cursor := ""
	for pageIndex := 0; pageIndex < 2; pageIndex++ {
		page, failure := watch.ResolveDownloadWatchTargets(t.Context(), source.Binding, hint, ports.DownloadWatchTargetQuery{Cursor: cursor})
		want := 64
		if pageIndex == 1 {
			want = 1
		}
		if failure != nil || len(page.Targets) != want || page.Complete != (pageIndex == 1) {
			t.Fatalf("page=%d targets=%d complete=%t error=%v", pageIndex, len(page.Targets), page.Complete, failure)
		}
		for _, target := range page.Targets {
			req, ok := expected[target.TaskID]
			if !ok || seen[target.TaskID] || target.OperationID != req.OperationID || target.Kind != "complete_tv_tracking_hint" || target.TrackingHintCapture == nil {
				t.Fatal("wrong or duplicate independent task")
			}
			capture := target.TrackingHintCapture.TrackingHintRequest()
			if capture.PriorJobID != req.Lease.ID || capture.TaskID != req.TaskID || capture.OperationID != req.OperationID || capture.HintID != hint.Metadata.EventID || capture.InstanceID != hint.Metadata.InstanceID || !capture.ReceivedAt.Equal(hint.Metadata.ReceivedAt) {
				t.Fatal("pagination rebound predecessor or event")
			}
			seen[target.TaskID] = true
		}
		cursor = page.NextCursor
		if (pageIndex == 0) == (cursor == "") {
			t.Fatal("invalid continuation boundary")
		}
	}
	if len(seen) != 65 || before != tvWatchSnapshot(t, frozen) {
		t.Fatal("target omitted or read changed protected graph")
	}
	t.Log("M8_COMPLETE_TV_TARGET_PAGING_PASS targets=65 pages=64+1 independent_operations=true predecessor_frozen=true read_only=true")
}
