package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

type completeTVFixture struct {
	*coverageFixture
	service *acquisition.CompleteTVService
	command ports.AcquireCompleteTVCoverageCommand
}

func newCompleteTVFixture(t *testing.T) *completeTVFixture {
	t.Helper()
	f := newCoverageFixture(t)
	client := uuid.New()
	f.exec(t, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted,remote_path_map) VALUES($1,'complete TV','qbittorrent','http://fixture.invalid','fixture',$2,'{}')`, client, []byte("encrypted-fixture"))
	f.exec(t, `UPDATE subscription SET state='active',downloader_id=$1 WHERE id=$2`, client, f.f.sub.Id)
	f.exec(t, `UPDATE library_root SET health='online',baseline_completed=true WHERE id=(SELECT root_id FROM subscription WHERE id=$1)`, f.f.sub.Id)
	ctx := context.Background()
	refresh := postgres.NewTVRefreshStore(f.f.a.DBPool(), idgen.New())
	saved, e := f.f.a.Queries().GetSubscriptionByID(ctx, f.f.sub.Id)
	if e != nil {
		t.Fatal(e)
	}
	input, _, e := refresh.ReadTVRefreshContext(ctx, f.actor, saved.ID, ports.TVRefreshCommand{ExpectedVersion: saved.IntentVersion}, "aired-metadata", "aired-metadata")
	if e != nil {
		t.Fatal(e)
	}
	aired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seasons := []ports.TVSeasonMetadata{{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{{TMDBEpisodeID: 1001, EpisodeNumber: 1, AirDate: &aired}, {TMDBEpisodeID: 1002, EpisodeNumber: 2, AirDate: &aired}}}}
	if _, e = refresh.ApplyTVRefresh(ctx, f.actor, input, ports.TVRefreshCommand{ExpectedVersion: input.IntentVersion, Seasons: []int{1}}, seasons, "aired-metadata", "aired-metadata"); e != nil {
		t.Fatal(e)
	}
	profile, _ := json.Marshal(quality.Profile{Resolution: "1080p", ResolutionParserVersion: quality.ResolutionTokenParserVersion, Source: "WEB-DL", SourceParserVersion: quality.SourceTokenParserVersion})
	f.exec(t, `UPDATE release_candidate SET quality_profile=$1 WHERE id=$2`, profile, f.candidate)
	v := f.preview(t)
	if !v.CanReview {
		t.Fatal(v.HoldReason)
	}
	review, e := f.svc.Review(context.Background(), f.actor, f.command(v), "complete-tv-review")
	if e != nil {
		t.Fatal(e)
	}
	return &completeTVFixture{f, acquisition.NewCompleteTVService(postgres.NewCompleteTVAcquisitionStore(f.f.a.DBPool(), idgen.New())), ports.AcquireCompleteTVCoverageCommand{SubscriptionID: f.f.sub.Id, ReviewID: review.ID, ExpectedInputStamp: v.InputStamp, ExpectedMaterialStamp: v.MaterialStamp, Confirmed: true}}
}
func (f *completeTVFixture) acquire(ctx context.Context, key string) (*ports.AcquireResult, error) {
	return f.service.Acquire(ctx, f.actor, f.command, key)
}
func (f *completeTVFixture) work(t *testing.T) [9]int {
	t.Helper()
	var c [9]int
	e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM complete_tv_execution_authority),(SELECT count(*) FROM acquisition_reservation),(SELECT count(*) FROM unit_reservation),(SELECT count(*) FROM download_requirement),(SELECT count(*) FROM job WHERE kind='complete_tv_coverage_download_submission'),(SELECT count(*) FROM audit_log WHERE action='complete_tv.acquisition.reserve'),(SELECT count(*) FROM outbox_event WHERE event_type='complete_tv.acquisition.reserved'),(SELECT count(*) FROM idempotency_record WHERE idempotency_key LIKE 'complete-tv.acquire:%'),(SELECT count(*) FROM download_task)`).Scan(&c[0], &c[1], &c[2], &c[3], &c[4], &c[5], &c[6], &c[7], &c[8])
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestCompleteTVAcquisitionPGAtomicEightWayAndReuse(t *testing.T) {
	f := newCompleteTVFixture(t)
	ctx := context.Background()
	for _, table := range []string{"unit_reservation", "job", "audit_log", "outbox_event", "idempotency_record"} {
		t.Run("late-"+table, func(t *testing.T) {
			before := f.work(t)
			f.exec(t, `CREATE FUNCTION fail_complete_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private late failure'; END $$`)
			f.exec(t, "CREATE TRIGGER fail_complete_write BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION fail_complete_write()")
			_, e := f.acquire(ctx, "one")
			if !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal("late failure", e)
			}
			f.exec(t, "DROP TRIGGER fail_complete_write ON "+table)
			f.exec(t, "DROP FUNCTION fail_complete_write()")
			if f.work(t) != before {
				t.Fatal("partial write", table, f.work(t), before)
			}
		})
	}
	var wg sync.WaitGroup
	results := make(chan *ports.AcquireResult, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := f.acquire(ctx, "one"); results <- r; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *ports.AcquireResult
	for r := range results {
		if r == nil || r.Status != "queued" || len(r.Reservations) != 2 {
			t.Fatal(r)
		}
		if first != nil && (r.OperationID != first.OperationID || r.JobID != first.JobID) {
			t.Fatal("duplicate work")
		}
		first = r
	}
	want := [9]int{1, 2, 2, 2, 1, 1, 1, 1, 0}
	if f.work(t) != want {
		t.Fatal(f.work(t), want)
	}
	r, e := f.acquire(ctx, "exact-authority")
	if e != nil || r.OperationID != first.OperationID || r.JobID != first.JobID {
		t.Fatal("exact reuse", r, e)
	}
	var raw []byte
	if e = f.f.a.DBPool().QueryRow(ctx, "SELECT snapshot FROM complete_tv_execution_authority WHERE operation_id=$1", r.OperationID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var snap ports.CompleteTVCoverageExecutionSnapshot
	if json.Unmarshal(raw, &snap) != nil || len(snap.Units) != 2 || len(snap.CompleteSeasons) != 1 || snap.Units[0].EpisodeID <= 0 || snap.Units[0].UnitVersion <= 0 || snap.CandidateSize != 1024 || snap.ActorID.String() != f.actor.ID || snap.Mapping.Mode != "standard" {
		t.Fatal("incomplete frozen authority", string(raw))
	}
	if _, e = f.f.a.DBPool().Exec(ctx, "UPDATE complete_tv_execution_authority SET snapshot='{}' WHERE operation_id=$1", r.OperationID); e == nil {
		t.Fatal("authority mutable")
	}
	f.exec(t, "UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1", f.candidate)
	if historical, e := f.acquire(ctx, "one"); e != nil || historical.OperationID != r.OperationID {
		t.Fatal("historical acquisition receipt", e)
	}
	if _, e = f.acquire(ctx, "stale-new"); !errors.Is(e, ports.ErrCompleteTVCoverageAcquisitionConflict) {
		t.Fatal("new command used stale review", e)
	}
}

func TestCompleteTVAcquisitionPGStaleAndMissing(t *testing.T) {
	mutations := []struct{ name, sql string }{
		{"size", `UPDATE release_candidate SET size_bytes=size_bytes+1 WHERE id=$1`},
		{"size-zero", `UPDATE release_candidate SET size_bytes=0 WHERE id=$1`},
		{"cipher", `UPDATE release_candidate SET link_encrypted='changed'::bytea WHERE id=$1`},
		{"title-coverage", `UPDATE release_candidate SET title='Mapping.Browser.Show.2020.S01E01.1080p.WEB-DL.mkv' WHERE id=$1`},
		{"source-expired", `UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes' WHERE indexer_id=(SELECT indexer_id FROM release_candidate WHERE id=$1)`},
		{"source-config", `UPDATE indexer SET config_version=config_version+1 WHERE id=(SELECT indexer_id FROM release_candidate WHERE id=$1)`},
		{"source-offline", `UPDATE indexer SET health='offline' WHERE id=(SELECT indexer_id FROM release_candidate WHERE id=$1)`},
		{"source-missing", `DELETE FROM indexer_query_candidate WHERE candidate_id=$1`},
		{"proof-missing", `UPDATE media SET metadata_version=metadata_version+1 WHERE id=(SELECT media_id FROM subscription WHERE id=$2)`},
		{"provider-identity", `UPDATE media_identity SET external_id='101' WHERE media_id=(SELECT media_id FROM subscription WHERE id=$2) AND namespace='tmdb.tv'`},
		{"unit-version", `UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=(SELECT unit_id FROM wanted_item WHERE subscription_id=$2 ORDER BY unit_id LIMIT 1)`},
		{"unit-retired", `UPDATE media_unit SET retired=true WHERE id=(SELECT unit_id FROM wanted_item WHERE subscription_id=$2 ORDER BY unit_id LIMIT 1)`},
		{"unselected", `UPDATE wanted_item SET is_skipped=true,user_skipped=true WHERE subscription_id=$2`},
		{"unreleased", `UPDATE media_unit SET air_date=clock_timestamp()+interval '1 day' WHERE media_id=(SELECT media_id FROM subscription WHERE id=$2)`},
		{"unknown-air-date", `UPDATE media_unit SET air_date=NULL WHERE media_id=(SELECT media_id FROM subscription WHERE id=$2)`},
		{"policy", `UPDATE subscription SET quality_policy_revision=quality_policy_revision+1 WHERE id=$2`},
		{"mapping", `WITH revision AS (INSERT INTO episode_mapping_revision(subscription_id,revision,media_id,media_version,intent_version,mode,input_stamp,source_hash,source_snapshot,mappings) SELECT s.id,1,s.media_id,m.metadata_version,s.intent_version+1,'standard',repeat('a',64),repeat('b',64),'{}','[]' FROM subscription s JOIN media m ON m.id=s.media_id WHERE s.id=$2 RETURNING subscription_id) UPDATE subscription SET episode_mapping_revision=1,intent_version=intent_version+1 WHERE id IN (SELECT subscription_id FROM revision)`},
		{"intent", `UPDATE subscription SET intent_version=intent_version+1 WHERE id=$2`},
		{"paused", `UPDATE subscription SET state='paused' WHERE id=$2`},
		{"root-generation", `UPDATE library_root SET generation=generation+1 WHERE id=(SELECT root_id FROM subscription WHERE id=$2)`},
		{"root-offline", `UPDATE library_root SET health='offline' WHERE id=(SELECT root_id FROM subscription WHERE id=$2)`},
		{"root-baseline", `UPDATE library_root SET baseline_completed=false WHERE id=(SELECT root_id FROM subscription WHERE id=$2)`},
		{"downloader-unconfigured", `UPDATE subscription SET downloader_id=NULL WHERE id=$2`},
		{"downloader-material", `UPDATE download_client SET remote_path_map='[{"remote":"/changed","local":"/changed"}]' WHERE id=(SELECT downloader_id FROM subscription WHERE id=$2)`},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			f := newCompleteTVFixture(t)
			before := f.work(t)
			f.exec(t, func() string {
				if m.name == "mapping" {
					return strings.Replace(m.sql, "WITH revision AS", "WITH fixture AS (SELECT $1::uuid,$2::uuid), revision AS", 1)
				}
				return "WITH fixture AS (SELECT $1::uuid,$2::uuid) " + m.sql
			}(), f.candidate, f.f.sub.Id)
			if _, e := f.acquire(context.Background(), m.name); e == nil {
				t.Fatal("stale authorization accepted")
			}
			if f.work(t) != before {
				t.Fatal("stale leaked work", f.work(t))
			}
		})
	}
}

func waitCompleteTVLock(t *testing.T, f *completeTVFixture, fragment string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var yes bool
		e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)`, "%"+fragment+"%").Scan(&yes)
		if e != nil {
			t.Fatal(e)
		}
		if yes {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("lock wait not observed", fragment)
}
func TestCompleteTVAcquisitionPGAccountWaitAndCancellation(t *testing.T) {
	for _, mode := range []string{"disabled", "demoted", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			f := newCompleteTVFixture(t)
			p := f.f.a.DBPool()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			before := f.work(t)
			tx, e := p.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			sql := "UPDATE app_user SET is_active=false WHERE id=$1"
			if mode == "demoted" {
				sql = "UPDATE app_user SET role='member' WHERE id=$1"
			}
			if _, e = tx.Exec(ctx, sql, uuid.MustParse(f.actor.ID)); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, e := f.acquire(ctx, "wait"); done <- e }()
			waitCompleteTVLock(t, f, "app_user")
			probe, e := p.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = probe.Exec(ctx, "SELECT id FROM library_root WHERE id=(SELECT root_id FROM subscription WHERE id=$1) FOR UPDATE NOWAIT", f.f.sub.Id); e != nil {
				t.Fatal("root locked ahead of account", e)
			}
			_ = probe.Rollback(ctx)
			if mode == "canceled" {
				cancel()
			} else if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			if mode == "canceled" {
				if !errors.Is(e, context.Canceled) {
					t.Fatal(e)
				}
			} else if !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("revoked account", e)
			}
			if f.work(t) != before {
				t.Fatal("revocation leaked writes")
			}
		})
	}
}

func (f *completeTVFixture) single(t *testing.T) func(context.Context) (*ports.AcquireResult, error) {
	t.Helper()
	ctx := context.Background()
	cand, query := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO release_candidate(id,indexer_id,guid,title,link_encrypted,size_bytes,quality_profile,source_config_version) SELECT $1,indexer_id,'single-contention','Mapping.Browser.Show.2020.S01E01.1080p.WEB-DL.mkv',link_encrypted,512,quality_profile,source_config_version FROM release_candidate WHERE id=$2`, cand, f.candidate)
	f.exec(t, `INSERT INTO indexer_query(id,indexer_id,query_type,keyword,page,config_version,state,results_count,finished_at) SELECT $1,indexer_id,'search','fixture',1,source_config_version,'succeeded',1,clock_timestamp() FROM release_candidate WHERE id=$2`, query, cand)
	f.exec(t, `INSERT INTO indexer_query_candidate(query_id,candidate_id) VALUES($1,$2)`, query, cand)
	refresh := postgres.NewTVRefreshStore(f.f.a.DBPool(), idgen.New())
	mapping := postgres.NewEpisodeMappingStore(postgres.NewEpisodeSourceStore(refresh), idgen.New())
	v, e := mapping.ReadEpisodeCandidate(ctx, f.actor, f.f.sub.Id, cand)
	if e != nil {
		t.Fatal(e)
	}
	svc := subscription.NewEpisodeCandidateReviewService(mapping)
	r, e := svc.Review(ctx, f.actor, ports.EpisodeCandidateReviewCommand{SubscriptionID: f.f.sub.Id, CandidateID: cand, ExpectedInputStamp: v.InputStamp, Confirmed: true, Reason: "explicit single"}, "single-review")
	if e != nil {
		t.Fatal(e)
	}
	repo := postgres.NewEpisodeAcquisitionStore(f.f.a.DBPool(), idgen.New())
	return func(ctx context.Context) (*ports.AcquireResult, error) {
		return repo.AcquireReviewedEpisode(ctx, f.actor, ports.AcquireReviewedEpisodeCommand{SubscriptionID: f.f.sub.Id, ReviewID: r.ID, ExpectedInputStamp: r.InputStamp, Confirmed: true}, "single-acquire")
	}
}
func TestCompleteTVAcquisitionPGPackageSingleContentionBothOrders(t *testing.T) {
	for _, packageFirst := range []bool{true, false} {
		t.Run(fmt.Sprint("package-first-", packageFirst), func(t *testing.T) {
			f := newCompleteTVFixture(t)
			single := f.single(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			tx, e := f.f.a.DBPool().Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, e = tx.Exec(ctx, "SELECT id FROM library_root WHERE id=(SELECT root_id FROM subscription WHERE id=$1) FOR UPDATE", f.f.sub.Id); e != nil {
				t.Fatal(e)
			}
			first := make(chan error, 1)
			second := make(chan error, 1)
			pack := func(ctx context.Context) (*ports.AcquireResult, error) { return f.acquire(ctx, "package") }
			run1, run2 := pack, single
			if !packageFirst {
				run1, run2 = single, pack
			}
			go func() { _, e := run1(ctx); first <- e }()
			waitCompleteTVLock(t, f, "library_root")
			go func() { _, e := run2(ctx); second <- e }()
			waitCompleteTVLock(t, f, "FROM policy")
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			if e = <-first; e != nil {
				t.Fatal("first failed", e)
			}
			if e = <-second; e == nil {
				t.Fatal("both acquired")
			}
			got := f.work(t)
			if packageFirst {
				if got[0] != 1 || got[1] != 2 || got[2] != 2 || got[3] != 2 {
					t.Fatal(got)
				}
			} else if got[0] != 0 || got[1] != 1 || got[2] != 1 || got[3] != 1 || got[4] != 0 {
				t.Fatal("partial package", got)
			}
		})
	}
}

func TestCompleteTVAcquisitionPGExpiredEvidenceAtCommit(t *testing.T) {
	f := newCompleteTVFixture(t)
	// Independently fixed expiry relative to DB time; pause a late receipt write,
	// long after authority/reservations/job were inserted, then expire provenance.
	f.exec(t, `UPDATE indexer_query SET finished_at=clock_timestamp()-interval '30 minutes'+interval '2 seconds' WHERE indexer_id=$1`, f.indexer)
	v := f.preview(t)
	r, e := f.svc.Review(context.Background(), f.actor, f.coverageFixture.command(v), "near-expiry")
	if e != nil {
		t.Fatal(e)
	}
	f.command = ports.AcquireCompleteTVCoverageCommand{SubscriptionID: f.f.sub.Id, ReviewID: r.ID, ExpectedInputStamp: v.InputStamp, ExpectedMaterialStamp: v.MaterialStamp, Confirmed: true}
	f.exec(t, `CREATE FUNCTION wait_complete_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key LIKE 'complete-tv.acquire:%' THEN PERFORM pg_sleep(2.2); END IF; RETURN NEW; END $$`)
	f.exec(t, `CREATE TRIGGER wait_complete_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION wait_complete_receipt()`)
	before := f.work(t)
	_, e = f.acquire(context.Background(), "expiry")
	if !errors.Is(e, ports.ErrCompleteTVCoverageAcquisitionConflict) {
		t.Fatal("expired evidence committed", e)
	}
	if f.work(t) != before {
		t.Fatal("late expiry partial writes")
	}
}

func TestCompleteTVAcquisitionPGUnknownPhysicalProtection(t *testing.T) {
	f := newCompleteTVFixture(t)
	r, e := f.acquire(context.Background(), "first")
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path) SELECT $1,$2,downloader_id,$3,'private-unknown-hash','unknown','/private-fixture' FROM subscription WHERE id=$4`, uuid.New(), r.OperationID, f.candidate, f.f.sub.Id)
	f.exec(t, `UPDATE unit_reservation SET expires_at=clock_timestamp()-interval '1 day' WHERE operation_id=$1`, r.OperationID)
	before := f.work(t)
	if _, e = f.acquire(context.Background(), "unknown-reuse"); e == nil {
		t.Fatal("unknown reauthorized")
	}
	if f.work(t) != before {
		t.Fatal("unknown protection changed")
	}
	if replay, e := f.acquire(context.Background(), "first"); e != nil || replay.OperationID != r.OperationID {
		t.Fatal("lost ack", e)
	}
	f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
	if _, e = f.acquire(context.Background(), "first"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("receipt bypassed live account", e)
	}
}

func TestCompleteTVAcquisitionPGLegacyPreparationCannotSubmit(t *testing.T) {
	f := newCompleteTVFixture(t)
	ctx := context.Background()
	result, err := f.acquire(ctx, "legacy-preparation-refusal")
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = f.f.a.DBPool().QueryRow(ctx, `SELECT snapshot FROM complete_tv_execution_authority WHERE operation_id=$1`, result.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var frozen ports.CompleteTVCoverageExecutionSnapshot
	if err = json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	var jobID uuid.UUID
	if err = f.f.a.DBPool().QueryRow(ctx, `UPDATE job SET state='running',lease_owner='legacy-complete-probe',lease_token=123,lease_until=clock_timestamp()+interval '1 minute' WHERE kind='complete_tv_coverage_download_submission' AND payload->>'operation_id'=$1 RETURNING id`, result.OperationID.String()).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	var cipher []byte
	var infoHash, savePath string
	if err = f.f.a.DBPool().QueryRow(ctx, `SELECT c.link_encrypted,COALESCE(c.info_hash,''),r.path FROM release_candidate c JOIN library_root r ON r.id=$2 WHERE c.id=$1`, frozen.CandidateID, frozen.RootID).Scan(&cipher, &infoHash, &savePath); err != nil {
		t.Fatal(err)
	}
	before := f.work(t)
	receipt, err := postgres.NewDownloadSubmissionStore(f.f.a.DBPool(), idgen.New()).PrepareSubmission(ctx, ports.DownloadSubmissionCommand{
		Lease: ports.JobLease{ID: jobID, Owner: "legacy-complete-probe", Token: 123}, TaskID: uuid.New(), OperationID: result.OperationID, ClientID: frozen.ClientID, CandidateID: frozen.CandidateID, InfoHash: infoHash, SavePath: savePath, CipherHash: sha256.Sum256(cipher),
	})
	if receipt != nil || !errors.Is(err, ports.ErrDownloadDemandChanged) {
		t.Fatal("legacy preparation consumed whole-set authority", receipt, err)
	}
	if after := f.work(t); before != after {
		t.Fatal("legacy refusal changed protected work", before, after)
	}
	var retained int
	if err = f.f.a.DBPool().QueryRow(ctx, `SELECT count(*) FROM unit_reservation WHERE operation_id=$1 AND state='reserved'`, result.OperationID).Scan(&retained); err != nil || retained != len(frozen.Units) {
		t.Fatal("legacy refusal released reserved units", retained, err)
	}
}
