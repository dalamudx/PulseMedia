package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	storedownload "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	storetransfer "github.com/dalamudx/PulseMedia/internal/store/transfer"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	transfercase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/google/uuid"
)

func TestReviewedEpisodeAcquisitionAtomicReplay(t *testing.T) {
	for _, c := range []struct {
		name                           string
		stale, other, mapped, absolute bool
	}{{"recovery", false, false, false, false}, {"late-metadata", true, false, false, false}, {"other-unit-metadata", true, true, false, false}, {"mapped-recovery", false, false, true, false}, {"mapped-other-unit", true, true, true, false}, {"absolute-recovery", false, false, true, true}, {"absolute-other-unit", true, true, true, true}} {
		t.Run(c.name, func(t *testing.T) { reviewedEpisodeAcquisitionProbe(t, c.stale, c.other, c.mapped, c.absolute) })
	}
}
func reviewedEpisodeAcquisitionProbe(t *testing.T, stale bool, otherUnit bool, mapped bool, absolute bool, browserProbe ...bool) {
	f := setupEpisodeMappingFixture(t)
	ctx := context.Background()
	p := f.a.DBPool()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := p.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	sub, e := f.a.Queries().GetSubscriptionByID(ctx, f.sub.Id)
	if e != nil {
		t.Fatal(e)
	}
	exec("INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'TV fixture','qbittorrent','http://fixture.invalid','fixture',$2,'{}')", uuid.New(), []byte("fixture"))
	clients, e := f.a.Queries().ListDownloadClients(ctx)
	if e != nil || len(clients) == 0 {
		t.Fatal(e)
	}
	exec("UPDATE library_root SET health='online',baseline_completed=true WHERE id=$1", sub.RootID)
	exec("UPDATE media SET title='Fixture',year=2020,metadata_version=metadata_version+1 WHERE id=$1", sub.MediaID)
	exec("UPDATE media_unit SET air_date='2020-01-01',metadata_version=metadata_version+1 WHERE media_id=$1", sub.MediaID)
	idx, candidate, query := uuid.New(), uuid.New(), uuid.New()
	exec("INSERT INTO indexer(id,name,url,api_key_encrypted,categories,health) VALUES($1,'fixture','http://fixture.invalid',$2,'[]','online')", idx, []byte("fixture"))
	var version int64
	if e = p.QueryRow(ctx, "SELECT config_version FROM indexer WHERE id=$1", idx).Scan(&version); e != nil {
		t.Fatal(e)
	}
	bound, _ := json.Marshal([]uuid.UUID{idx})
	exec("UPDATE subscription SET state='active',indexer_ids=$2,downloader_id=$3,intent_version=intent_version+1 WHERE id=$1", sub.ID, bound, clients[0].ID)
	resourceTitle := "Fixture.2020.S01E01.1080p.BluRay.mkv"
	sourceFile := "Fixture.S01E01.mkv"
	if mapped {
		draft := ports.EpisodeMappingDraft{Mode: "manual", Entries: []ports.ManualEpisodeMappingEntry{{SourceNumber: 25, UnitID: f.units[0]}, {SourceNumber: 27, UnitID: f.units[1]}}}
		if absolute {
			draft = ports.EpisodeMappingDraft{Mode: "absolute", GroupID: "group1", PartID: "part1"}
		}
		path := "/api/v1/subscriptions/" + sub.ID.String() + "/episode-mapping"
		w := f.call(f.admin, "POST", path+"/preview", "", draft)
		var preview ports.EpisodeMappingPreview
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || !preview.CanPublish {
			t.Fatal("mapped preview", w.Code, w.Body.String())
		}
		command := ports.EpisodeMappingCommand{Draft: draft, ExpectedInputStamp: preview.InputStamp, ExpectedSourceHash: preview.SourceHash, Confirmed: true}
		if w = f.call(f.admin, "POST", path, "mapped-publish", command); w.Code != 200 {
			t.Fatal("mapped publish", w.Code, w.Body.String())
		}
		resourceTitle = "[Team] Fixture - 25 [1080p BluRay x264].mkv"
		if absolute {
			resourceTitle = "[Team] Fixture - 1 [1080p BluRay x264].mkv"
		}
		sourceFile = resourceTitle
	}

	profile, _ := json.Marshal(quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion, Codec: quality.CodecH264, CodecParserVersion: quality.CodecTokenParserVersion})
	exec("INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) VALUES($1,$2,'episode-test',$6,$3,4096,$4,$5)", candidate, idx, []byte("fixture"), profile, version, resourceTitle)
	exec("INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count,finished_at) VALUES($1,$2,'search','Fixture',1,$3,'succeeded',1,clock_timestamp())", query, idx, version)
	exec("INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)", query, candidate)
	var observed subscription.MappedReleaseExplanation
	w := f.call(f.admin, "POST", "/api/v1/subscriptions/"+sub.ID.String()+"/episode-mapping/explain-release", "", map[string]any{"candidate_id": candidate})
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &observed) != nil || observed.TargetReason != "missing" || observed.QualityDecision != "allow" {
		t.Fatal("not eligible", w.Code, w.Body.String())
	}
	exec("UPDATE indexer SET next_query_at=clock_timestamp()+interval '1 minute' WHERE id=$1", idx)
	w = f.call(f.admin, "POST", "/api/v1/subscriptions/"+sub.ID.String()+"/episode-mapping/explain-release", "", map[string]any{"candidate_id": candidate})
	var budgetHeld subscription.MappedReleaseExplanation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &budgetHeld) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	exec("UPDATE indexer SET next_query_at=clock_timestamp()-interval '1 minute' WHERE id=$1", idx)
	w = f.call(f.admin, "POST", "/api/v1/subscriptions/"+sub.ID.String()+"/episode-mapping/explain-release", "", map[string]any{"candidate_id": candidate})
	var budgetReady subscription.MappedReleaseExplanation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &budgetReady) != nil || budgetReady.CandidateInputStamp != budgetHeld.CandidateInputStamp || budgetReady.CandidateInputStamp != observed.CandidateInputStamp {
		t.Fatal("query-budget clock boundary changed manual review digest", w.Code)
	}
	if !mapped {
		for _, date := range []struct{ name, sql, reason string }{{"future", "clock_timestamp()+interval '2 days'", "future_air_date"}, {"unknown", "NULL", "air_date_unknown"}} {
			exec("UPDATE media_unit SET air_date="+date.sql+",metadata_version=metadata_version+1 WHERE id=$1", f.units[0])
			response := f.call(f.admin, "POST", "/api/v1/subscriptions/"+sub.ID.String()+"/episode-mapping/explain-release", "", map[string]any{"candidate_id": candidate})
			var held subscription.MappedReleaseExplanation
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &held) != nil || held.TargetReason != date.reason {
				t.Fatal("unreleased target not waiting", date.name, response.Code, response.Body.String())
			}
			response = f.call(f.admin, "POST", "/api/v1/subscriptions/"+sub.ID.String()+"/episode-candidate-reviews", "held-"+date.name, map[string]any{"candidate_id": candidate, "expected_input_stamp": held.CandidateInputStamp, "confirmed": true, "reason": "核对当前日期"})
			var heldReview ports.EpisodeCandidateReviewReceipt
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &heldReview) != nil || heldReview.Status != "reviewed_not_executable" {
				t.Fatal("review gained execution authority", date.name, response.Code, response.Body.String())
			}
			response = f.call(f.admin, "POST", "/api/v1/subscriptions/"+sub.ID.String()+"/episode-acquisitions", "held-"+date.name, map[string]any{"review_id": heldReview.ID, "expected_input_stamp": heldReview.InputStamp, "confirmed": true})
			if response.Code != 409 {
				t.Fatal("unreleased target acquisition granted", date.name, response.Code, response.Body.String())
			}
			var writes int
			if err := p.QueryRow(ctx, "SELECT (SELECT count(*) FROM episode_execution_authority)+(SELECT count(*) FROM unit_reservation)+(SELECT count(*) FROM job WHERE kind='episode_download_submission')").Scan(&writes); err != nil || writes != 0 {
				t.Fatal("unreleased target created authority", writes, err)
			}
		}
		exec("UPDATE media_unit SET air_date='2020-01-01',metadata_version=metadata_version+1 WHERE id=$1", f.units[0])
		w = f.call(f.admin, "POST", "/api/v1/subscriptions/"+sub.ID.String()+"/episode-mapping/explain-release", "", map[string]any{"candidate_id": candidate})
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &observed) != nil || observed.TargetReason != "missing" {
			t.Fatal("mature target not recovered", w.Code, w.Body.String())
		}
	}
	var review ports.EpisodeCandidateReviewReceipt
	w = f.call(f.admin, "POST", "/api/v1/subscriptions/"+sub.ID.String()+"/episode-candidate-reviews", "episode-acq-review", map[string]any{"candidate_id": candidate, "expected_input_stamp": observed.CandidateInputStamp, "confirmed": true, "reason": "确认当前标准单集身份"})
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &review) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	repo := postgres.NewEpisodeAcquisitionStore(p, idgen.New())
	actor := &ports.Actor{ID: sub.UserID.String(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	cmd := ports.AcquireReviewedEpisodeCommand{SubscriptionID: sub.ID, ReviewID: review.ID, ExpectedInputStamp: review.InputStamp, Confirmed: true}
	counts := func(want int) {
		t.Helper()
		var authorities, lanes, reservations, requirements, jobs int
		e := p.QueryRow(ctx, "SELECT (SELECT count(*) FROM episode_execution_authority),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM job WHERE kind='episode_download_submission')").Scan(&authorities, &lanes, &reservations, &requirements, &jobs)
		if e != nil || authorities != want || lanes != want || reservations != want || requirements != want || jobs != want {
			t.Fatal("atomic counts", authorities, lanes, reservations, requirements, jobs, e)
		}
	}
	exec("CREATE FUNCTION fail_episode_acquire_nonce() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key LIKE 'episode.acquire:%' THEN RAISE EXCEPTION 'fixture';END IF;RETURN NEW;END$$")
	exec("CREATE TRIGGER fail_episode_acquire_nonce BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_episode_acquire_nonce()")
	acquirePath := "/api/v1/subscriptions/" + sub.ID.String() + "/episode-acquisitions"
	acquireBody := map[string]any{"review_id": review.ID, "expected_input_stamp": review.InputStamp, "confirmed": true}
	if w := f.call(f.admin, "POST", acquirePath, "one", acquireBody); w.Code != 503 {
		t.Fatal("public nonce fault", w.Code, w.Body.String())
	}
	if _, e = repo.AcquireReviewedEpisode(ctx, actor, cmd, "one"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("missing failure", e)
	}
	counts(0)
	exec("DROP TRIGGER fail_episode_acquire_nonce ON idempotency_record")
	exec("DROP FUNCTION fail_episode_acquire_nonce()")
	var wg sync.WaitGroup
	results := make(chan *ports.AcquireResult, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := repo.AcquireReviewedEpisode(ctx, actor, cmd, "one")
			results <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal("concurrent", e)
		}
	}
	var op uuid.UUID
	for r := range results {
		if op == uuid.Nil {
			op = r.OperationID
		}
		if r.OperationID != op {
			t.Fatal("duplicate acquisition")
		}
	}
	counts(1)
	record, e := postgres.Queries(p).GetEpisodeExecutionAuthority(ctx, op)
	var numberingSeal ports.EpisodeExecutionSnapshot
	if e != nil || json.Unmarshal(record.Snapshot, &numberingSeal) != nil || len(numberingSeal.MappingInputStamp) != 64 || (!mapped && numberingSeal.MappingSourceHash != "") || (mapped && (len(numberingSeal.MappingSourceHash) != 64 || numberingSeal.SourceNumber != map[bool]int{false: 25, true: 1}[absolute] || len(numberingSeal.MappingRows) != 2 || numberingSeal.Kind != "reviewed_mapped_episode")) || (numberingSeal.MappingValidUntil != nil) != absolute {
		t.Fatal("current standard mapping provenance not frozen", e)
	}
	if w := f.call(f.member, "POST", acquirePath, "one", acquireBody); w.Code != 403 {
		t.Fatal("member replay authorized", w.Code)
	}
	w = f.call(f.admin, "POST", acquirePath, "one", acquireBody)
	var publicReceipt ports.AcquireResult
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &publicReceipt) != nil || publicReceipt.OperationID != op || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("public samekey receipt", w.Code, w.Body.String())
	}
	if !mapped {
		mapping := postgres.NewEpisodeMappingStore(postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(p, idgen.New())), idgen.New())
		for _, mutation := range []string{"is_active=false", "role='member'"} {
			exec("UPDATE app_user SET "+mutation+" WHERE id=$1", sub.UserID)
			if _, err := repo.AcquireReviewedEpisode(ctx, actor, cmd, "one"); !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("stale account replayed acquisition", mutation, err)
			}
			if _, err := mapping.ReadEpisodeCandidate(ctx, actor, sub.ID, candidate); !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("stale account read candidate", mutation, err)
			}
			if _, err := mapping.ReadEpisodeCandidateReviewReceipt(ctx, actor, ports.EpisodeCandidateReviewCommand{SubscriptionID: sub.ID}, "unused", strings.Repeat("a", 64)); !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("stale account read review receipt", mutation, err)
			}
			exec("UPDATE app_user SET is_active=true,role='admin' WHERE id=$1", sub.UserID)
		}
		if recovered, err := repo.AcquireReviewedEpisode(ctx, actor, cmd, "one"); err != nil || recovered.OperationID != op {
			t.Fatal("original operation not recovered", recovered, err)
		}
		counts(1)
	}
	badBody := map[string]any{"review_id": review.ID, "expected_input_stamp": review.InputStamp, "confirmed": true, "download_url": "http://fixture.invalid"}
	if w := f.call(f.admin, "POST", acquirePath, "bad", badBody); w.Code != 400 {
		t.Fatal("unknown request field accepted", w.Code)
	}
	var jobID uuid.UUID
	var token int64
	if e = p.QueryRow(ctx, "UPDATE job SET state='running',lease_owner='episode-owner',lease_until=clock_timestamp()+interval '5 minutes',lease_token=lease_token+1 WHERE kind='episode_download_submission' AND payload->>'operation_id'=$1 RETURNING id,lease_token", op.String()).Scan(&jobID, &token); e != nil {
		t.Fatal(e)
	}
	cand, e := f.a.Queries().GetCandidateByID(ctx, candidate)
	if e != nil {
		t.Fatal(e)
	}
	root, e := f.a.Queries().GetLibraryRootByID(ctx, sub.RootID)
	if e != nil {
		t.Fatal(e)
	}
	submit := ports.DownloadSubmissionCommand{Lease: ports.JobLease{ID: jobID, Owner: "episode-owner", Token: token}, TaskID: uuid.New(), OperationID: op, ClientID: clients[0].ID, CandidateID: candidate, InfoHash: cand.InfoHash.String, SavePath: root.Path, CipherHash: sha256.Sum256(cand.LinkEncrypted)}
	submission := postgres.NewDownloadSubmissionStore(p, idgen.New())
	oldToken := submit
	oldToken.Lease.Token++
	if _, e = submission.PrepareSubmission(ctx, oldToken); !errors.Is(e, ports.ErrDownloadDemandChanged) {
		t.Fatal("old episode token authorized Add", e)
	}
	exec("UPDATE download_client SET url='http://changed.invalid' WHERE id=$1", clients[0].ID)
	if _, e = submission.PrepareSubmission(ctx, submit); !errors.Is(e, ports.ErrDownloadDemandChanged) {
		t.Fatal("changed client authorized Add", e)
	}
	exec("UPDATE download_client SET url=$2 WHERE id=$1", clients[0].ID, clients[0].Url)
	exec("CREATE FUNCTION fail_episode_prepare() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='download.submission_prepared' THEN RAISE EXCEPTION 'fixture';END IF;RETURN NEW;END$$")
	exec("CREATE TRIGGER fail_episode_prepare BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_episode_prepare()")
	if !mapped {
		holder, lockErr := p.Begin(ctx)
		if lockErr != nil {
			t.Fatal(lockErr)
		}
		defer func() { _ = holder.Rollback(ctx) }()
		if _, lockErr = holder.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", sub.UserID); lockErr != nil {
			t.Fatal(lockErr)
		}
		workerCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		done := make(chan error, 1)
		go func() { _, prepareErr := submission.PrepareSubmission(workerCtx, submit); done <- prepareErr }()
		joined := false
		defer func() {
			cancel()
			_ = holder.Rollback(ctx)
			if !joined {
				<-done
			}
		}()
		deadline := time.Now().Add(3 * time.Second)
		for {
			var waiting bool
			if lockErr = p.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%SELECT id,is_active,role FROM app_user%')").Scan(&waiting); lockErr != nil {
				t.Fatal(lockErr)
			}
			if waiting {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("worker never waited on current user")
			}
			time.Sleep(5 * time.Millisecond)
		}
		// A command holding the user must still be able to acquire the policy.
		policyTx, policyErr := p.Begin(ctx)
		if policyErr != nil {
			t.Fatal(policyErr)
		}
		policyCtx, stopPolicy := context.WithTimeout(ctx, time.Second)
		_, policyErr = policyTx.Exec(policyCtx, "SELECT id FROM policy WHERE id=$1 FOR UPDATE", sub.QualityPolicyID)
		stopPolicy()
		_ = policyTx.Rollback(ctx)
		if policyErr != nil {
			t.Fatal("worker held policy while waiting for user", policyErr)
		}
		if _, lockErr = holder.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", sub.UserID); lockErr != nil {
			t.Fatal(lockErr)
		}
		if lockErr = holder.Commit(ctx); lockErr != nil {
			t.Fatal(lockErr)
		}
		prepareErr := <-done
		joined = true
		if !errors.Is(prepareErr, ports.ErrDownloadDemandChanged) {
			t.Fatal("post-wait inactive user authorized submission", prepareErr)
		}
		exec("UPDATE app_user SET is_active=true WHERE id=$1", sub.UserID)
	}

	if _, e = submission.PrepareSubmission(ctx, submit); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("prepare audit did not rollback", e)
	}
	var taskCount int
	if e = p.QueryRow(ctx, "SELECT count(*) FROM download_task WHERE operation_id=$1", op).Scan(&taskCount); e != nil || taskCount != 0 {
		t.Fatal("prepare leaked task", taskCount, e)
	}
	exec("DROP TRIGGER fail_episode_prepare ON audit_log")
	exec("DROP FUNCTION fail_episode_prepare()")
	exec("CREATE FUNCTION expire_episode_prepare() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='download.submission_prepared' THEN UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE kind='episode_download_submission' AND state='running';END IF;RETURN NEW;END$$")
	exec("CREATE TRIGGER expire_episode_prepare BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION expire_episode_prepare()")
	if _, e = submission.PrepareSubmission(ctx, submit); !errors.Is(e, ports.ErrDownloadDemandChanged) {
		t.Fatal("expired final prepared commit accepted", e)
	}
	var expiredTasks int
	if e = p.QueryRow(ctx, "SELECT count(*) FROM download_task WHERE operation_id=$1", op).Scan(&expiredTasks); e != nil || expiredTasks != 0 {
		t.Fatal("expired prepared task leaked", expiredTasks, e)
	}
	counts(1)
	exec("DROP TRIGGER expire_episode_prepare ON audit_log")
	exec("DROP FUNCTION expire_episode_prepare()")

	if absolute {
		// Disposable-database clock seam: only this transaction advances after final audit, without waiting six hours.
		exec("UPDATE job SET lease_until=clock_timestamp()+interval '1 day' WHERE id=$1", jobID)
		exec("CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE sql AS $$SELECT pg_catalog.clock_timestamp()+COALESCE(NULLIF(current_setting('pulse.fixture_clock_shift',true),''),'0 seconds')::interval$$")
		exec("CREATE FUNCTION expire_mapping_after_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='download.submission_prepared' THEN PERFORM set_config('search_path','public,pg_catalog',true);PERFORM set_config('pulse.fixture_clock_shift','7 hours',true);END IF;RETURN NEW;END$$")
		exec("CREATE TRIGGER expire_mapping_after_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION expire_mapping_after_audit()")
		if _, e = submission.PrepareSubmission(ctx, submit); !errors.Is(e, ports.ErrDownloadDemandChanged) {
			t.Fatal("source TTL expired after final audit but prepared committed", e)
		}
		if e = p.QueryRow(ctx, "SELECT count(*) FROM download_task WHERE operation_id=$1", op).Scan(&expiredTasks); e != nil || expiredTasks != 0 {
			t.Fatal("TTL-expired prepared task leaked", expiredTasks, e)
		}
		counts(1)
		exec("DROP TRIGGER expire_mapping_after_audit ON audit_log")
		exec("DROP FUNCTION expire_mapping_after_audit()")
		exec("DROP FUNCTION public.clock_timestamp()")
	}

	prepared, e := submission.PrepareSubmission(ctx, submit)
	if e != nil || prepared.TaskID != submit.TaskID {
		t.Fatal("current episode did not prepare", prepared, e)
	}
	if _, e = submission.CompleteSubmission(ctx, oldToken.Lease, submit.TaskID, false); !errors.Is(e, ports.ErrDownloadDemandChanged) {
		t.Fatal("old completion token", e)
	}
	observedUnknown, e := submission.CompleteSubmission(ctx, submit.Lease, submit.TaskID, false)
	if e != nil || observedUnknown.State != "unknown" {
		t.Fatal("unknown observation discarded", observedUnknown, e)
	}
	counts(1)

	hash := strings.Repeat("a", 40)
	exec("UPDATE download_task SET state='tracking',info_hash=$2 WHERE id=$1", submit.TaskID, hash)
	client := &inventoryObservationClient{info: ports.TorrentInfo{InfoHash: hash, SavePath: root.Path, ContentPath: filepath.Join(root.Path, sourceFile), Progress: 1, TotalBytes: 32, DownloadedBytes: 32, State: "seeding"}, files: []ports.TorrentFile{{Index: 0, Name: sourceFile, SizeBytes: 32, Progress: 1}}}
	manager := storedownload.NewManager(p, idgen.New(), make([]byte, 32))
	manager.RegisterClient(clients[0].ID, client)
	client.changedOnSecond = true
	if _, e = manager.ObserveTask(ctx, op); !errors.Is(e, ports.ErrDownloadReconcilePending) {
		t.Fatal("mixed identity inventory saved", e)
	}
	client.changedOnSecond = false
	client.files[0].Progress = 0.5
	if _, e = manager.ObserveTask(ctx, op); !errors.Is(e, ports.ErrDownloadReconcilePending) {
		t.Fatal("partial inventory saved", e)
	}
	client.files[0].Progress = 1
	exec("CREATE FUNCTION fail_episode_completion() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='download.completed' THEN RAISE EXCEPTION 'fixture';END IF;RETURN NEW;END$$")
	exec("CREATE TRIGGER fail_episode_completion BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_episode_completion()")
	if _, e = manager.ObserveTask(ctx, op); e == nil {
		t.Fatal("completion failure ignored")
	}
	var inventories, organizations int
	var taskState string
	if e = p.QueryRow(ctx, "SELECT (SELECT count(*) FROM download_file_inventory),(SELECT count(*) FROM job WHERE kind='download_organization'),state FROM download_task WHERE id=$1", submit.TaskID).Scan(&inventories, &organizations, &taskState); e != nil || inventories != 0 || organizations != 0 || taskState != "tracking" {
		t.Fatal("inventory/completion transaction leaked", inventories, organizations, taskState, e)
	}
	exec("DROP TRIGGER fail_episode_completion ON audit_log")
	exec("DROP FUNCTION fail_episode_completion()")
	completed, e := manager.ObserveTask(ctx, op)
	if e != nil || completed.State != "completed" {
		t.Fatal("inventory completion", completed, e)
	}
	if e = p.QueryRow(ctx, "SELECT (SELECT count(*) FROM download_file_inventory),(SELECT count(*) FROM job WHERE kind='download_organization')").Scan(&inventories, &organizations); e != nil || inventories != 1 || organizations != 1 {
		t.Fatal("missing inventory/job", inventories, organizations, e)
	}
	if _, e = p.Exec(ctx, "UPDATE download_file_inventory SET files='[]' WHERE task_id=$1", submit.TaskID); e == nil {
		t.Fatal("inventory resigned")
	}
	if _, e = manager.ObserveTask(ctx, op); e != nil {
		t.Fatal(e)
	}
	counts(1)

	sourceBytes := bytes.Repeat([]byte("V"), 32)
	if e = os.WriteFile(client.info.ContentPath, sourceBytes, 0600); e != nil {
		t.Fatal(e)
	}
	marker := root.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	if e = os.WriteFile(filepath.Join(root.Path, marker), []byte("fixture root"), 0600); e != nil {
		t.Fatal(e)
	}
	var organizationID uuid.UUID
	var organizationToken int64
	if e = p.QueryRow(ctx, "UPDATE job SET state='running',lease_owner='tv-organizer',lease_token=lease_token+1,lease_until=clock_timestamp()+interval '5 minutes' WHERE kind='download_organization' AND payload->>'task_id'=$1 RETURNING id,lease_token", submit.TaskID.String()).Scan(&organizationID, &organizationToken); e != nil {
		t.Fatal(e)
	}
	svc := storetransfer.NewService(p, idgen.New(), outbox.New(p, idgen.New(), clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
	if absolute {
		exec("CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE sql AS $$SELECT pg_catalog.clock_timestamp()+COALESCE(NULLIF(current_setting('pulse.fixture_clock_shift',true),''),'0 seconds')::interval$$")
		exec("CREATE FUNCTION fail_tv_import_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='transfer.imported' THEN UPDATE job SET lease_until=pg_catalog.clock_timestamp()+interval '1 day' WHERE kind='download_organization' AND state='running';UPDATE transfer_plan SET lease_until=pg_catalog.clock_timestamp()+interval '1 day' WHERE id=(SELECT plan_id FROM transfer_step WHERE id=NEW.target_id::uuid);PERFORM set_config('search_path','public,pg_catalog',true);PERFORM set_config('pulse.fixture_clock_shift','7 hours',true);END IF;RETURN NEW;END$$")
	} else {
		exec("CREATE FUNCTION fail_tv_import_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='transfer.imported' THEN RAISE EXCEPTION 'fixture';END IF;RETURN NEW;END$$")
	}

	exec("CREATE TRIGGER fail_tv_import_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_tv_import_audit()")
	if e = svc.OrganizeDownload(ctx, ports.JobLease{ID: organizationID, Owner: "tv-organizer", Token: organizationToken}); e != nil && !errors.Is(e, ports.ErrTransferReviewRequired) && !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("import audit failure handling", e)
	}
	if absolute && e != nil {
		t.Fatal("TTL fault must be classified as review, not an unrelated SQL failure", e)
	}
	if absolute {
		var state string
		if e = p.QueryRow(ctx, "SELECT organization_state FROM download_task WHERE id=$1", submit.TaskID).Scan(&state); e != nil || state != "review_required" {
			t.Fatal("expired TTL not held for review", state, e)
		}
	}
	var importedFiles, protected int
	if e = p.QueryRow(ctx, "SELECT (SELECT count(*) FROM library_file_unit WHERE unit_id=$1),(SELECT count(*) FROM acquisition_reservation WHERE operation_id=$2 AND released_at IS NULL)", f.units[0], op).Scan(&importedFiles, &protected); e != nil || importedFiles != 0 || protected != 1 {
		t.Fatal("failed import leaked facts or protection", importedFiles, protected, e)
	}
	var originalPlan uuid.UUID
	if e = p.QueryRow(ctx, "SELECT transfer_plan_id FROM download_task WHERE id=$1", submit.TaskID).Scan(&originalPlan); e != nil {
		t.Fatal("original TV plan lost", e)
	}
	exec("DROP TRIGGER fail_tv_import_audit ON audit_log")
	exec("DROP FUNCTION fail_tv_import_audit()")
	if absolute {
		exec("DROP FUNCTION public.clock_timestamp()")
	}
	recoveryRepo := storetransfer.NewRepository(p, idgen.New(), outbox.New(p, idgen.New(), clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
	if preview, e := recoveryRepo.PreviewOriginalTransferRecovery(ctx, actor, originalPlan); e != nil || preview.Recoverable {
		t.Fatal("inflight lease/job recovery authorized", preview, e)
	}
	// Emulate the owned worker's finished review outcome, without approving or rewriting any frozen plan evidence.
	exec("UPDATE download_task SET organization_state='review_required' WHERE id=$1", submit.TaskID)
	exec("UPDATE job SET state='succeeded',lease_owner=NULL,lease_until=NULL WHERE id=$1", organizationID)
	exec("UPDATE transfer_plan SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", originalPlan)
	preview, e := recoveryRepo.PreviewOriginalTransferRecovery(ctx, actor, originalPlan)
	if e != nil || !preview.Recoverable || preview.Reason != "original_hardlink_confirmed" {
		t.Fatal("original recovery preview", preview, e)
	}
	command := ports.TransferRecoveryCommand{PlanID: originalPlan, ExpectedInputStamp: preview.InputStamp, Confirmed: true, Reason: "核对原硬链接事实后恢复原冻结计划"}
	if !mapped && len(browserProbe) == 0 {
		assertOriginalRecoveryFreshness(t, p, actor, command)
		assertRecoveryUserLock(t, p, actor, func(ctx context.Context) error {
			_, e := recoveryRepo.PreviewOriginalTransferRecovery(ctx, actor, originalPlan)
			return e
		})
	}
	sourcePath := client.info.ContentPath
	backupPath := sourcePath + ".owned-recovery-backup"
	if e = os.Rename(sourcePath, backupPath); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(sourcePath, []byte("replacement inode"), 0600); e != nil {
		t.Fatal(e)
	}
	heldPreview, e := recoveryRepo.PreviewOriginalTransferRecovery(ctx, actor, originalPlan)
	if e != nil || heldPreview.Recoverable || heldPreview.Reason != "source_identity_changed" {
		t.Fatal("source replacement incorrectly recoverable", heldPreview, e)
	}
	if _, e = recoveryRepo.CommitOriginalTransferRecovery(ctx, actor, command, "replaced-source"); !errors.Is(e, ports.ErrTransferReviewRequired) {
		t.Fatal("replacement source recovered old plan", e)
	}
	if e = os.Remove(sourcePath); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(backupPath, sourcePath); e != nil {
		t.Fatal(e)
	}
	// A contradictory target remains untouched; recovery cannot replace it with a newly signed file identity.
	var targetPath string
	if e = p.QueryRow(ctx, "SELECT target_path FROM transfer_step WHERE plan_id=$1", originalPlan).Scan(&targetPath); e != nil {
		t.Fatal(e)
	}
	targetBackup := targetPath + ".owned-recovery-backup"
	if e = os.Rename(targetPath, targetBackup); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(targetPath, []byte("unrelated target"), 0600); e != nil {
		t.Fatal(e)
	}
	heldPreview, e = recoveryRepo.PreviewOriginalTransferRecovery(ctx, actor, originalPlan)
	if e != nil || heldPreview.Recoverable || heldPreview.Reason != "target_identity_unconfirmed" {
		t.Fatal("contradictory target recoverable", heldPreview, e)
	}
	if _, e = recoveryRepo.CommitOriginalTransferRecovery(ctx, actor, command, "conflicting-target"); !errors.Is(e, ports.ErrTransferReviewRequired) {
		t.Fatal("conflicting target overwritten", e)
	}
	contradictory, e := os.ReadFile(targetPath)
	if e != nil || string(contradictory) != "unrelated target" {
		t.Fatal("recovery mutated contradictory file", e)
	}
	if e = os.Remove(targetPath); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(targetBackup, targetPath); e != nil {
		t.Fatal(e)
	}

	recoveryPath := "/api/v1/transfer-plans/" + originalPlan.String() + "/recovery"
	if w := f.call(f.member, "GET", recoveryPath, "", nil); w.Code != 403 {
		t.Fatal("member observed original recovery", w.Code)
	}
	w = f.call(f.admin, "GET", recoveryPath, "", nil)
	var publicPreview ports.TransferRecoveryPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &publicPreview) != nil || !publicPreview.Recoverable || publicPreview.InputStamp != preview.InputStamp {
		t.Fatal("recovery public preview", w.Code, w.Body.String())
	}
	publicCommand := map[string]any{"expected_input_stamp": preview.InputStamp, "confirmed": true, "reason": command.Reason}
	exec("CREATE FUNCTION fail_recovery_nonce() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key LIKE 'transfer.recovery:%' THEN RAISE EXCEPTION 'fixture';END IF;RETURN NEW;END$$")
	exec("CREATE TRIGGER fail_recovery_nonce BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_recovery_nonce()")
	if w := f.call(f.admin, "POST", recoveryPath, "recover-one", publicCommand); w.Code != 503 {
		t.Fatal("public recovery nonce fault", w.Code, w.Body.String())
	}
	if _, e = recoveryRepo.CommitOriginalTransferRecovery(ctx, actor, command, "recover-one"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("recovery failure ignored", e)
	}
	var recoveryJobs, requested, importEvents int
	var orgState string
	if e = p.QueryRow(ctx, "SELECT (SELECT count(*) FROM job WHERE kind='download_organization'),(SELECT count(*) FROM outbox_event WHERE event_type='transfer.recovery_requested'),(SELECT count(*) FROM outbox_event WHERE event_type='media.imported'),organization_state FROM download_task WHERE id=$1", submit.TaskID).Scan(&recoveryJobs, &requested, &importEvents, &orgState); e != nil || recoveryJobs != 1 || requested != 0 || importEvents != 0 || orgState != "review_required" {
		t.Fatal("failed recovery leaked", recoveryJobs, requested, importEvents, orgState, e)
	}
	exec("DROP TRIGGER fail_recovery_nonce ON idempotency_record")
	exec("DROP FUNCTION fail_recovery_nonce()")
	var recovered *ports.TransferRecoveryReceipt
	recoveryKey := "recover-one"
	if len(browserProbe) > 0 && browserProbe[0] {
		result := runOriginalTransferRecoveryBrowser(t, f, op, originalPlan)
		recovered = &result.Receipt
		recoveryKey = result.Key
	} else {
		var recoveryWG sync.WaitGroup
		recoveryResults := make(chan *ports.TransferRecoveryReceipt, 8)
		recoveryErrs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			recoveryWG.Add(1)
			go func() {
				defer recoveryWG.Done()
				r, e := recoveryRepo.CommitOriginalTransferRecovery(ctx, actor, command, "recover-one")
				recoveryResults <- r
				recoveryErrs <- e
			}()
		}
		recoveryWG.Wait()
		close(recoveryResults)
		close(recoveryErrs)
		for e := range recoveryErrs {
			if e != nil {
				t.Fatal("recovery replay", e)
			}
		}
		for r := range recoveryResults {
			if recovered == nil {
				recovered = r
			}
			if r == nil || r.PlanID != originalPlan || r.JobID != recovered.JobID {
				t.Fatal("recovery duplicated plan/job", r)
			}
		}
	}
	w = f.call(f.admin, "POST", recoveryPath, recoveryKey, publicCommand)
	var publicRecovery ports.TransferRecoveryReceipt
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &publicRecovery) != nil || publicRecovery.JobID != recovered.JobID || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("public recovery original receipt", w.Code, w.Body.String())
	}
	if !mapped && len(browserProbe) == 0 {
		assertRecoveryUserLock(t, p, actor, func(ctx context.Context) error {
			_, e := recoveryRepo.CommitOriginalTransferRecovery(ctx, actor, command, recoveryKey)
			return e
		})
	}
	if w := f.call(f.member, "POST", recoveryPath, recoveryKey, publicCommand); w.Code != 403 {
		t.Fatal("member replay recovery accepted", w.Code)
	}
	badRecovery := map[string]any{"expected_input_stamp": preview.InputStamp, "confirmed": true, "reason": command.Reason, "source_path": "/outside"}
	if w := f.call(f.admin, "POST", recoveryPath, "bad-body", badRecovery); w.Code != 400 {
		t.Fatal("recovery unknown source field", w.Code)
	}
	if e = p.QueryRow(ctx, "SELECT (SELECT count(*) FROM job WHERE kind='download_organization'),(SELECT count(*) FROM outbox_event WHERE event_type='transfer.recovery_requested'),(SELECT count(*) FROM outbox_event WHERE event_type='media.imported')").Scan(&recoveryJobs, &requested, &importEvents); e != nil || recoveryJobs != 2 || requested != 1 || importEvents != 0 {
		t.Fatal("recovery faked import or duplicate", recoveryJobs, requested, importEvents, e)
	}
	organizationID = recovered.JobID
	if e = p.QueryRow(ctx, "UPDATE job SET state='running',lease_owner='tv-organizer',lease_token=lease_token+1,lease_until=clock_timestamp()+interval '5 minutes' WHERE id=$1 RETURNING lease_token", organizationID).Scan(&organizationToken); e != nil {
		t.Fatal(e)
	}

	if stale {
		changed := f.units[0]
		if otherUnit {
			changed = f.units[1]
		}
		exec("UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", changed)
		if e = svc.OrganizeDownload(ctx, ports.JobLease{ID: organizationID, Owner: "tv-organizer", Token: organizationToken}); e != nil && !errors.Is(e, ports.ErrTransferReviewRequired) {
			t.Fatal(e)
		}
		var mappings, holds int
		if e = p.QueryRow(ctx, "SELECT (SELECT count(*) FROM library_file_unit WHERE unit_id=$1),(SELECT count(*) FROM acquisition_reservation WHERE operation_id=$2 AND released_at IS NULL)", f.units[0], op).Scan(&mappings, &holds); e != nil || mappings != 0 || holds != 1 {
			t.Fatal("stale TV plan imported or released unknown link", mappings, holds, e)
		}
		var keptPlan uuid.UUID
		if e = p.QueryRow(ctx, "SELECT transfer_plan_id FROM download_task WHERE id=$1", submit.TaskID).Scan(&keptPlan); e != nil || keptPlan != originalPlan {
			t.Fatal("stale plan silently replaced", keptPlan, e)
		}
		return
	}

	if e = svc.OrganizeDownload(ctx, ports.JobLease{ID: organizationID, Owner: "tv-organizer", Token: organizationToken}); e != nil {
		t.Fatal("TV organization", e)
	}
	var fileID uuid.UUID
	var rel string
	if e = p.QueryRow(ctx, "SELECT f.id,f.rel_path FROM library_file f JOIN library_file_unit fu ON fu.file_id=f.id WHERE fu.unit_id=$1 AND f.root_id=$2 AND f.retired_at IS NULL AND fu.retired_at IS NULL", f.units[0], root.ID).Scan(&fileID, &rel); e != nil {
		t.Fatal("TV organization did not import", e)
	}
	targetBytes, e := os.ReadFile(filepath.Join(root.Path, rel))
	if e != nil || !bytes.Equal(sourceBytes, targetBytes) {
		t.Fatal("TV bytes not imported", e)
	}
	sourceStat, e := os.Stat(client.info.ContentPath)
	if e != nil {
		t.Fatal(e)
	}
	targetStat, e := os.Stat(filepath.Join(root.Path, rel))
	if e != nil || !os.SameFile(sourceStat, targetStat) {
		t.Fatal("not a hardlink", e)
	}
	wantRel, e := naming.FormatEpisodeRel("Fixture", 2020, 1, 1, quality.Profile{Resolution: quality.Res1080p, ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: quality.SourceBluray, SourceParserVersion: quality.SourceTokenParserVersion, Codec: quality.CodecH264, CodecParserVersion: quality.CodecTokenParserVersion}, ".mkv")
	if e != nil || rel != wantRel {
		t.Fatal("wrong canonical path", rel, wantRel, e)
	}
	var pending, active int
	if e = p.QueryRow(ctx, "SELECT (SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND state='pending'),(SELECT count(*) FROM acquisition_reservation WHERE operation_id=$1 AND released_at IS NULL)", op).Scan(&pending, &active); e != nil || pending != 0 || active != 0 {
		t.Fatal("successful TV import did not consume demand", pending, active, e)
	}

	if _, e = p.Exec(ctx, "UPDATE episode_execution_authority SET snapshot='{}' WHERE operation_id=$1", op); e == nil {
		t.Fatal("authority resigned")
	}
	exec("UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1", candidate)
	if r, e := repo.AcquireReviewedEpisode(ctx, actor, cmd, "one"); e != nil || r.OperationID != op {
		t.Fatal("lost ack replay", r, e)
	}
	if _, e = repo.AcquireReviewedEpisode(ctx, actor, cmd, "new"); e == nil {
		t.Fatal("new stale acquisition accepted")
	}
	counts(1)
	if _, e = repo.AcquireReviewedEpisode(ctx, &ports.Actor{ID: actor.ID, Type: ports.ActorType("api_token"), Role: ports.RoleAdmin}, cmd, "one"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("token privilege replay", e)
	}
}
