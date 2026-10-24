package httpapi_test

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Self-authored fixed raw bencode fixture. Two independent explicit files and
// fixed lengths; expected provider identities come from the fixed metadata input,
// never from the parser/inspector output. Not part of the parser golden corpus.
const completeTVRaw = "d4:infod5:filesld6:lengthi512e4:pathl15:Show.S01E01.mkveed6:lengthi512e4:pathl15:Show.S01E02.mkveee4:name4:Show12:piece lengthi1024e6:pieces20:abcdefghijklmnopqrstee"

type prepareTVFixture struct {
	*completeTVFixture
	c     ports.PrepareCompleteTVSubmissionCommand
	store *postgres.CompleteTVSubmissionStore
	raw   []byte
}

func newPrepareTVFixture(t *testing.T, beforeAcquire ...func(*completeTVFixture)) *prepareTVFixture {
	t.Helper()
	f := newCompleteTVFixture(t)
	if len(beforeAcquire) > 0 {
		beforeAcquire[0](f)
		v := f.preview(t)
		r, e := f.svc.Review(context.Background(), f.actor, f.coverageFixture.command(v), "prepared-review")
		if e != nil {
			t.Fatal(e)
		}
		f.command = ports.AcquireCompleteTVCoverageCommand{SubscriptionID: f.f.sub.Id, ReviewID: r.ID, ExpectedInputStamp: v.InputStamp, ExpectedMaterialStamp: v.MaterialStamp, Confirmed: true}
	}
	r, e := f.acquire(context.Background(), "prepare-acquire")
	if e != nil {
		t.Fatal(e)
	}
	f.exec(t, `UPDATE job SET state='running',lease_owner='private-prepare',lease_token=123,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, r.JobID)
	raw := []byte(completeTVRaw)
	proof, e := ports.InspectCompleteTVMetainfo(torrent.Inspector{}, raw)
	if e != nil {
		t.Fatal("raw fixture", e)
	}
	return &prepareTVFixture{completeTVFixture: f, c: ports.PrepareCompleteTVSubmissionCommand{Lease: ports.JobLease{ID: r.JobID, Owner: "private-prepare", Token: 123}, TaskID: uuid.New(), OperationID: r.OperationID, Metainfo: proof}, store: postgres.NewCompleteTVSubmissionStore(f.f.a.DBPool(), idgen.New()), raw: raw}
}
func (f *prepareTVFixture) prepare() (*ports.CompleteTVSubmissionReceipt, error) {
	return f.store.PrepareCompleteTVSubmission(context.Background(), f.c)
}
func (f *prepareTVFixture) preparedCounts(t *testing.T) [6]int {
	t.Helper()
	var c [6]int
	e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM download_task),(SELECT count(*) FROM complete_tv_submission_preparation),(SELECT count(*) FROM complete_tv_submission_unit),(SELECT count(*) FROM audit_log WHERE action='complete_tv.submission_prepared'),(SELECT count(*) FROM outbox_event WHERE event_type='complete_tv.submission_prepared'),(SELECT count(*) FROM unit_reservation WHERE state='dispatched')`).Scan(&c[0], &c[1], &c[2], &c[3], &c[4], &c[5])
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func (f *prepareTVFixture) rejectUnchanged(t *testing.T) {
	t.Helper()
	before := f.preparedCounts(t)
	work := f.work(t)
	r, e := f.prepare()
	if r != nil || !errors.Is(e, ports.ErrCompleteTVSubmissionReview) {
		t.Fatal("expected review", r, e)
	}
	if f.preparedCounts(t) != before || f.work(t) != work {
		t.Fatal("partial writes or protection changed")
	}
}
func TestCompleteTVPreparationPGWholeSetEightWay(t *testing.T) {
	f := newPrepareTVFixture(t)
	// Eight serialized preparations test deduplication, with room for race-build contention.
	// Production and timeout scenarios keep the default two-second lock wait.
	poolConfig := f.f.a.DBPool().Config()
	configureConnection := poolConfig.AfterConnect
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if configureConnection != nil {
			if err := configureConnection(ctx, conn); err != nil {
				return err
			}
		}
		_, err := conn.Exec(ctx, "SET lock_timeout='15s'")
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f.store = postgres.NewCompleteTVSubmissionStore(pool, idgen.New())
	var wg sync.WaitGroup
	results := make(chan *ports.CompleteTVSubmissionReceipt, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := f.prepare(); results <- r; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	first := 0
	for r := range results {
		if r == nil || r.TaskID != f.c.TaskID || r.State != "prepared_not_sent" {
			t.Fatal(r)
		}
		if !r.AlreadyExisted {
			first++
		}
	}
	if first != 1 || f.preparedCounts(t) != ([6]int{1, 1, 2, 1, 1, 2}) {
		t.Fatal(first, f.preparedCounts(t))
	}
	var raw []byte
	var hash string
	var digest []byte
	if e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT proof,info_hash,torrent_sha256 FROM complete_tv_submission_preparation`).Scan(&raw, &hash, &digest); e != nil {
		t.Fatal(e)
	}
	if hash != "d497dcf93e2dbd4b917ba68788ba7d42590cf594" || hex.EncodeToString(digest) != "03c6f9a90a3d138c8c1d652b71628b891a87b9ea89d678980cc2f68f0398966f" || strings.Contains(string(raw), "announce") {
		t.Fatal("wrong bytes proof")
	}
	rows, e := f.f.a.DBPool().Query(context.Background(), `SELECT unit_id,provider_episode_id,unit_key,video_path,video_size FROM complete_tv_submission_unit ORDER BY unit_key`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var id uuid.UUID
		var provider, size int64
		var key, path string
		if e = rows.Scan(&id, &provider, &key, &path, &size); e != nil {
			t.Fatal(e)
		}
		expectedKeys := []string{"S01E01", "S01E02"}
		expectedPaths := []string{"Show/Show.S01E01.mkv", "Show/Show.S01E02.mkv"}
		expectedProviders := []int64{1001, 1002}
		if i >= 2 || id != f.f.units[i] || provider != expectedProviders[i] || key != expectedKeys[i] || path != expectedPaths[i] || size != 512 {
			t.Fatal(id, provider, key, path, size)
		}
		i++
	}
	if rows.Err() != nil || i != 2 {
		t.Fatal(rows.Err(), i)
	}
	for _, sql := range []string{`UPDATE complete_tv_submission_preparation SET info_hash=repeat('0',40)`, `DELETE FROM complete_tv_submission_unit`} {
		if _, e = f.f.a.DBPool().Exec(context.Background(), sql); e == nil {
			t.Fatal("mutable proof")
		}
	}
	// The same leased replay cannot substitute changed bytes with the same info hash.
	changed := []byte("d7:comment1:x4:info" + strings.TrimSuffix(strings.TrimPrefix(completeTVRaw, "d4:info"), "e") + "e")
	p, e := ports.InspectCompleteTVMetainfo(torrent.Inspector{}, changed)
	if e != nil {
		t.Fatal(e)
	}
	f.c.Metainfo = p
	f.rejectUnchanged(t)
}
func TestCompleteTVPreparationPGScopeFences(t *testing.T) {
	cases := map[string]string{
		"job-missing":         `UPDATE job SET payload=jsonb_set(payload,'{unit_ids}',jsonb_build_array(payload->'unit_ids'->0)) WHERE kind='complete_tv_coverage_download_submission'`,
		"job-extra":           `UPDATE job SET payload=jsonb_set(payload,'{unit_ids}',payload->'unit_ids'||jsonb_build_array('00000000-0000-0000-0000-000000000001')) WHERE kind='complete_tv_coverage_download_submission'`,
		"job-duplicate":       `UPDATE job SET payload=jsonb_set(payload,'{unit_ids}',jsonb_build_array(payload->'unit_ids'->0,payload->'unit_ids'->0)) WHERE kind='complete_tv_coverage_download_submission'`,
		"fence-missing":       `UPDATE download_requirement SET submission_fence=NULL WHERE unit_id=(SELECT unit_id FROM download_requirement LIMIT 1)`,
		"fence-version":       `UPDATE download_requirement SET submission_fence=jsonb_set(submission_fence,'{unit_metadata_version}','999')`,
		"fence-extra":         `UPDATE download_requirement SET submission_fence=submission_fence||'{"unfrozen":true}'::jsonb`,
		"demand-missing":      `UPDATE download_requirement SET state='released' WHERE unit_id=(SELECT unit_id FROM download_requirement LIMIT 1)`,
		"demand-extra":        `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence) SELECT '00000000-0000-0000-0000-000000000002',operation_id,unit_id,root_id,'user','other',submission_fence FROM download_requirement LIMIT 1`,
		"lane-missing":        `UPDATE acquisition_reservation SET released_at=clock_timestamp() WHERE unit_id=(SELECT unit_id FROM acquisition_reservation LIMIT 1)`,
		"lane-foreign-task":   `UPDATE acquisition_reservation SET legacy_conflict=true`,
		"reservation-missing": `UPDATE unit_reservation SET state='released' WHERE unit_id=(SELECT unit_id FROM unit_reservation LIMIT 1)`,
		"reservation-expired": `UPDATE unit_reservation SET expires_at=clock_timestamp()`,
		"reservation-extra":   `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state,expires_at) SELECT '00000000-0000-0000-0000-000000000003',unit_id,root_id,'extra',candidate_id,operation_id,'reserved',expires_at FROM unit_reservation LIMIT 1`,
		"intent":              `UPDATE subscription SET intent_version=intent_version+1`,
		"metadata":            `UPDATE media SET metadata_version=metadata_version+1`,
		"unit-version":        `UPDATE media_unit SET metadata_version=metadata_version+1`,
		"provider":            `UPDATE media_identity SET external_id='101' WHERE namespace='tmdb.tv'`,
		"source-missing":      `DELETE FROM indexer_query_candidate`,
		"source-expired":      `UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes'`,
		"policy":              `UPDATE subscription SET quality_policy_revision=quality_policy_revision+1`,
		"mapping":             `WITH revision AS (INSERT INTO episode_mapping_revision(subscription_id,revision,media_id,media_version,intent_version,mode,input_stamp,source_hash,source_snapshot,mappings) SELECT s.id,1,s.media_id,m.metadata_version,s.intent_version+1,'standard',repeat('a',64),repeat('b',64),'{}','[]' FROM subscription s JOIN media m ON m.id=s.media_id RETURNING subscription_id) UPDATE subscription SET episode_mapping_revision=1,intent_version=intent_version+1 WHERE id IN (SELECT subscription_id FROM revision)`,

		"client":    `UPDATE download_client SET password_encrypted='changed'`,
		"root":      `UPDATE library_root SET generation=generation+1`,
		"candidate": `UPDATE release_candidate SET size_bytes=size_bytes+1`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) { f := newPrepareTVFixture(t); f.exec(t, sql); f.rejectUnchanged(t) })
	}
}
func TestCompleteTVPreparationPGMetainfo(t *testing.T) {
	for name, raw := range map[string]string{
		"expansion": strings.Replace(strings.Replace(completeTVRaw, "lengthi512e", "lengthi511e", 1), "15:Show.S01E02.mkvee", "15:Show.S01E02.mkveed6:lengthi1e4:pathl15:Show.S01E03.mkvee", 1),
		"shrink":    strings.Replace(completeTVRaw, "d6:lengthi512e4:pathl15:Show.S01E02.mkvee", "", 1),
		"unknown":   strings.Replace(completeTVRaw, "Show.S01E02.mkv", "Show.S01E02.zip", 1),
		"duplicate": strings.Replace(completeTVRaw, "Show.S01E02.mkv", "Show.S01E01.mkv", 1),
	} {
		t.Run(name, func(t *testing.T) {
			f := newPrepareTVFixture(t)
			svc := acquisition.NewCompleteTVSubmissionService(f.store, torrent.Inspector{})
			before := f.preparedCounts(t)
			r, e := svc.Prepare(context.Background(), f.c.Lease, f.c.TaskID, f.c.OperationID, []byte(raw))
			if r != nil || !errors.Is(e, ports.ErrCompleteTVSubmissionReview) || f.preparedCounts(t) != before {
				t.Fatal(r, e)
			}
		})
	}
	t.Run("candidate-hash", func(t *testing.T) {
		f := newPrepareTVFixture(t, func(f *completeTVFixture) { f.exec(t, `UPDATE release_candidate SET info_hash=repeat('0',40)`) })
		f.rejectUnchanged(t)
	})
	t.Run("unsealed", func(t *testing.T) {
		f := newPrepareTVFixture(t)
		f.c.Metainfo = &ports.CompleteTVMetainfoProof{}
		f.rejectUnchanged(t)
	})
	t.Run("owned-byte-copy", func(t *testing.T) {
		f := newPrepareTVFixture(t)
		f.raw[0] = 'x'
		copyBytes := f.c.Metainfo.Bytes()
		copyBytes[0] = 'x'
		files := f.c.Metainfo.Files()
		files[0].Path = "changed"
		if !f.c.Metainfo.ValidateBinding() {
			t.Fatal("evidence mutable")
		}
		svc := acquisition.NewCompleteTVSubmissionService(f.store, torrent.Inspector{})
		if _, e := svc.Prepare(context.Background(), f.c.Lease, f.c.TaskID, f.c.OperationID, f.c.Metainfo.Bytes()); e != nil {
			t.Fatal(e)
		}
	})
}
func TestCompleteTVPreparationPGLeaseUnknownAndRollback(t *testing.T) {
	for _, name := range []string{"owner", "token", "expired", "unknown", "submitting"} {
		t.Run(name, func(t *testing.T) {
			f := newPrepareTVFixture(t)
			switch name {
			case "owner":
				f.c.Lease.Owner = "wrong"
			case "token":
				f.c.Lease.Token++
			case "expired":
				f.exec(t, `UPDATE job SET lease_until=clock_timestamp()`)
			default:
				f.exec(t, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path) SELECT $1,$2,downloader_id,$3,'protected',$4,'/fixture' FROM subscription WHERE id=$5`, uuid.New(), f.c.OperationID, f.candidate, name, f.f.sub.Id)
			}
			f.rejectUnchanged(t)
		})
	}
	for _, table := range []string{"complete_tv_submission_preparation", "complete_tv_submission_unit", "audit_log", "outbox_event"} {
		t.Run("rollback-"+table, func(t *testing.T) {
			f := newPrepareTVFixture(t)
			before := f.preparedCounts(t)
			work := f.work(t)
			f.exec(t, `CREATE FUNCTION fail_prepare() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned fault'; END $$`)
			f.exec(t, "CREATE TRIGGER fail_prepare BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION fail_prepare()")
			r, e := f.prepare()
			if r != nil || !errors.Is(e, ports.ErrUnavailable) || f.preparedCounts(t) != before || f.work(t) != work {
				t.Fatal(r, e, "partial transaction")
			}
		})
	}
	for _, kind := range []string{"lease", "source", "reservation"} {
		t.Run("tail-expiry-"+kind, func(t *testing.T) {
			var f *prepareTVFixture
			switch kind {
			case "source":
				f = newPrepareTVFixture(t, func(f *completeTVFixture) {
					f.exec(t, `UPDATE indexer_query SET finished_at=clock_timestamp()-interval '30 minutes'+interval '5 seconds'`)
				})
			case "reservation":
				f = newPrepareTVFixture(t, func(f *completeTVFixture) {
					f.exec(t, `CREATE FUNCTION short_reservation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.expires_at=clock_timestamp()+interval '5 seconds'; RETURN NEW; END $$`)
					f.exec(t, `CREATE TRIGGER short_reservation BEFORE INSERT ON unit_reservation FOR EACH ROW EXECUTE FUNCTION short_reservation()`)
				})
			default:
				f = newPrepareTVFixture(t)
			}
			if kind == "lease" {
				f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '3 seconds'`)
			}
			delay := "3.2"
			if kind == "source" || kind == "reservation" {
				delay = "5.2"
			}
			f.exec(t, `CREATE SEQUENCE tail_probe`)
			f.exec(t, `CREATE FUNCTION wait_prepare() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='complete_tv.submission_prepared' THEN PERFORM nextval('tail_probe'); PERFORM pg_sleep(`+delay+`); END IF; RETURN NEW; END $$`)
			f.exec(t, `CREATE TRIGGER wait_prepare BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION wait_prepare()`)
			f.rejectUnchanged(t)
			var called bool
			if e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT is_called FROM tail_probe`).Scan(&called); e != nil || !called {
				t.Fatal("tail fault was not reached", e)
			}
		})
	}
}
func TestCompleteTVPreparationPGLockInvalidation(t *testing.T) {
	for _, kind := range []string{"account", "root"} {
		t.Run(kind, func(t *testing.T) {
			f := newPrepareTVFixture(t)
			ctx := context.Background()
			tx, e := f.f.a.DBPool().Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if kind == "account" {
				_, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
			} else {
				_, e = tx.Exec(ctx, `UPDATE library_root SET health='offline' WHERE id=(SELECT root_id FROM subscription WHERE id=$1)`, f.f.sub.Id)
			}
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, err := f.prepare(); done <- err }()
			deadline := time.Now().Add(time.Second)
			waiting := false
			for time.Now().Before(deadline) {
				var n int
				if e = f.f.a.DBPool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&n); e != nil {
					t.Fatal(e)
				}
				if n > 0 {
					waiting = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("did not observe lock wait")
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, ports.ErrCompleteTVSubmissionReview) {
				t.Fatal(e)
			}
			if f.preparedCounts(t) != ([6]int{}) {
				t.Fatal("wait invalidation wrote")
			}
		})
	}
}

func TestCompleteTVPreparationPGExtraProtection(t *testing.T) {
	for _, kind := range []string{"lane", "root-reservation"} {
		t.Run(kind, func(t *testing.T) {
			f := newPrepareTVFixture(t)
			if kind == "lane" {
				f.exec(t, `INSERT INTO media(id,title,media_type) VALUES('00000000-0000-0000-0000-000000000010','Unrelated Movie','movie')`)
				f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) VALUES('00000000-0000-0000-0000-000000000011','00000000-0000-0000-0000-000000000010','movie')`)
				f.exec(t, `INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,source_root_id,source_generation,source_stamp) SELECT '00000000-0000-0000-0000-000000000012','00000000-0000-0000-0000-000000000011',operation_id,candidate_id,source_root_id,source_generation,source_stamp FROM acquisition_reservation LIMIT 1`)
			} else {
				f.exec(t, `INSERT INTO library_root(id,path,media_type,name) VALUES('00000000-0000-0000-0000-000000000013','/unrelated','tv','unrelated')`)
				f.exec(t, `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state,expires_at) SELECT '00000000-0000-0000-0000-000000000014',unit_id,'00000000-0000-0000-0000-000000000013',purpose,candidate_id,operation_id,'reserved',expires_at FROM unit_reservation LIMIT 1`)
			}
			f.rejectUnchanged(t)
		})
	}
	t.Run("prepared-unknown", func(t *testing.T) {
		f := newPrepareTVFixture(t)
		if _, e := f.prepare(); e != nil {
			t.Fatal(e)
		}
		f.exec(t, `UPDATE download_task SET state='unknown'`)
		f.rejectUnchanged(t)
	})
}

func TestCompleteTVPreparationPGReplayReservationWaitExpiry(t *testing.T) {
	f := newPrepareTVFixture(t, func(f *completeTVFixture) {
		f.exec(t, `CREATE FUNCTION replay_short_reservation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.expires_at=clock_timestamp()+interval '5 seconds'; RETURN NEW; END $$`)
		f.exec(t, `CREATE TRIGGER replay_short_reservation BEFORE INSERT ON unit_reservation FOR EACH ROW EXECUTE FUNCTION replay_short_reservation()`)
	})
	if _, err := f.prepare(); err != nil {
		t.Fatal(err)
	}
	before := f.preparedCounts(t)
	// Allow the intended row wait to outlast the short reservation instead of
	// stopping at the application's default two-second lock timeout.
	cfg := f.f.a.DBPool().Config()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET statement_timeout='20s'; SET lock_timeout='10s'")
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := postgres.NewCompleteTVSubmissionStore(pool, idgen.New())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := f.f.a.DBPool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var expires time.Time
	if err = tx.QueryRow(ctx, `SELECT expires_at FROM unit_reservation WHERE operation_id=$1 ORDER BY unit_id LIMIT 1 FOR UPDATE`, f.c.OperationID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	type result struct {
		receipt *ports.CompleteTVSubmissionReceipt
		err     error
	}
	done := make(chan result, 1)
	go func() { r, e := store.PrepareCompleteTVSubmission(ctx, f.c); done <- result{r, e} }()
	received := false
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		cancel()
		if !received {
			<-done
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		var n int
		if err = f.f.a.DBPool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query ILIKE '%FROM unit_reservation%' AND query ILIKE '%FOR UPDATE%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			waiting = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("replay did not wait for reservation row")
	}
	if _, err = tx.Exec(ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.05)`, expires); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	received = true
	if got.receipt != nil || !errors.Is(got.err, ports.ErrCompleteTVSubmissionReview) {
		t.Fatal("expired reservation replay accepted", got.receipt, got.err)
	}
	if f.preparedCounts(t) != before {
		t.Fatal("expired replay changed physical protection")
	}
}

func TestCompleteTVPreparationPGInfrastructureFailures(t *testing.T) {
	t.Run("authority-unavailable", func(t *testing.T) {
		f := newPrepareTVFixture(t)
		f.f.a.DBPool().Close()
		r, err := f.prepare()
		if r != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("database failure misclassified", r, err)
		}
	})
	t.Run("account-lock-timeout", func(t *testing.T) {
		f := newPrepareTVFixture(t)
		ctx := context.Background()
		tx, err := f.f.a.DBPool().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		var id uuid.UUID
		if err = tx.QueryRow(ctx, `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, uuid.MustParse(f.actor.ID)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		cfg := f.f.a.DBPool().Config()
		cfg.ConnConfig.RuntimeParams["lock_timeout"] = "100ms"
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		before := f.preparedCounts(t)
		r, err := postgres.NewCompleteTVSubmissionStore(pool, idgen.New()).PrepareCompleteTVSubmission(ctx, f.c)
		if r != nil || !errors.Is(err, ports.ErrUnavailable) {
			t.Fatal("database lock timeout misclassified", r, err)
		}
		if f.preparedCounts(t) != before {
			t.Fatal("database lock timeout changed protection")
		}
	})
}

func TestCompleteTVPreparationPGLegacyPathProof(t *testing.T) {
	f := newPrepareTVFixture(t)
	if r, e := f.store.PrepareCompleteTVSubmission(context.Background(), f.c); e != nil || r == nil {
		t.Fatal(r, e)
	}
	f.exec(t, `ALTER TABLE complete_tv_submission_preparation DISABLE TRIGGER complete_tv_preparation_immutable`)
	f.exec(t, `UPDATE complete_tv_submission_preparation SET proof=jsonb_set(proof-'save_path','{kind}','"complete_tv_prepared_v1"')`)
	var before, after string
	if e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT proof::text FROM complete_tv_submission_preparation`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	counts := f.preparedCounts(t)
	if r, e := f.store.PrepareCompleteTVSubmission(context.Background(), f.c); r != nil || !errors.Is(e, ports.ErrCompleteTVSubmissionReview) {
		t.Fatal("legacy preparation upgraded by replay", r, e)
	}
	if e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT proof::text FROM complete_tv_submission_preparation`).Scan(&after); e != nil {
		t.Fatal(e)
	}
	if before != after || counts != f.preparedCounts(t) {
		t.Fatal("legacy immutable preparation or protection changed")
	}
}
