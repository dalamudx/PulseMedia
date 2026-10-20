package mediascrape

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type scrapeFixture struct {
	pool                           *pgxpool.Pool
	repo                           *Repository
	actor                          *ports.Actor
	root, file, unit, media, event uuid.UUID
}

func newScrapeFixture(t *testing.T) scrapeFixture {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	version, err := postgres.Migrate(t.Context(), dsn, "../../../migrations")
	if err != nil || version != postgres.CurrentMigrationVersion {
		t.Fatalf("migrate: version=%d err=%v", version, err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 8
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var server int
	if err = pool.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int").Scan(&server); err != nil || server != 180006 {
		t.Fatalf("PG18.6 required: %d %v", server, err)
	}
	f := scrapeFixture{pool: pool, repo: NewRepository(pool), root: uuid.New(), file: uuid.New(), unit: uuid.New(), media: uuid.New(), event: uuid.New()}
	f.actor = f.admin(t)
	// SQL-prepared facts only: no importer, scanner, sidecar worker or filesystem I/O is claimed.
	f.exec(t, `INSERT INTO media(id,title,original_title,year,media_type) VALUES($1,'SQL-prepared movie','Original',2024,'movie')`, f.media)
	f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,'movie')`, f.unit, f.media)
	f.exec(t, `INSERT INTO library_root(id,path,media_type,name,baseline_completed) VALUES($1,'/synthetic/scrape','movie','SQL-prepared library',true)`, f.root)
	f.exec(t, `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,'Movie/source.mkv',4096,123,'stat.v1:301:4096:123')`, f.file, f.root)
	f.exec(t, `INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)`, f.file, f.unit)
	f.exec(t, `INSERT INTO unit_fact(id,unit_id,root_id,source_file_id,stage) VALUES($1,$2,$3,$4,'imported')`, uuid.New(), f.unit, f.root, f.file)
	f.eventAt(t, f.event, 1, "2026-01-01T00:00:00Z")
	return f
}
func (f scrapeFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f scrapeFixture) admin(t *testing.T) *ports.Actor {
	t.Helper()
	a := &ports.Actor{ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	f.exec(t, `INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,$2,'SQL-prepared-no-login','admin',true)`, a.ID, a.ID)
	return a
}
func (f scrapeFixture) eventAt(t *testing.T, id uuid.UUID, seq int, at string) {
	t.Helper()
	f.exec(t, `INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq,created_at) VALUES($1,'media',$2,'media.imported',jsonb_build_object('file_id',$3::uuid,'unit_id',$4::uuid),$5,$6::timestamptz)`, id, f.unit.String(), f.file, f.unit, seq, at)
}
func (f scrapeFixture) job(t *testing.T, state, key, at string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	f.exec(t, `INSERT INTO job(id,kind,state,dedupe_key,payload,max_attempts,created_at) VALUES($1,'media_sidecar',$2,$3,jsonb_build_object('version',1,'source_event_id',$4::uuid,'file_id',$5::uuid,'unit_id',$6::uuid),1,$7::timestamptz)`, id, state, key, f.event, f.file, f.unit, at)
	return id
}
func (f scrapeFixture) candidates(t *testing.T) []ports.MediaScrapeCandidate {
	t.Helper()
	c, err := f.repo.ListMediaScrapeCandidates(t.Context(), f.actor)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func (f scrapeFixture) counts(t *testing.T) [4]int {
	t.Helper()
	var n [4]int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM media_scrape_batch),(SELECT count(*) FROM job),(SELECT count(*) FROM media_scrape_item),(SELECT count(*) FROM audit_log)`).Scan(&n[0], &n[1], &n[2], &n[3]); err != nil {
		t.Fatal(err)
	}
	return n
}

// Snapshot every public table to detect business writes by GETs and failed commands.
func (f scrapeFixture) graph(t *testing.T) string {
	t.Helper()
	rows, err := f.pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename <> 'goose_db_version' ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, name := range names {
		var data string
		if err = f.pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) v FROM `+pgx.Identifier{name}.Sanitize()+` t) s`).Scan(&data); err != nil {
			t.Fatal(err)
		}
		out.WriteString(name)
		out.WriteString(data)
	}
	return out.String()
}
func (f scrapeFixture) start(t *testing.T, key string) ports.MediaScrapeProgress {
	t.Helper()
	p, reused, err := f.repo.StartMediaScrape(t.Context(), f.actor, key, uuid.New)
	if err != nil || reused {
		t.Fatalf("start %s: %+v reused=%v err=%v", key, p, reused, err)
	}
	return p
}

func TestScrapeCandidatesRequireCurrentImportedEvidence(t *testing.T) {
	f := newScrapeFixture(t)
	newer := uuid.MustParse("ffffffff-ffff-4fff-bfff-ffffffffffff")
	f.eventAt(t, uuid.MustParse("00000000-0000-4000-8000-000000000001"), 2, "2026-01-02T00:00:00Z")
	f.eventAt(t, newer, 3, "2026-01-02T00:00:00Z")
	// A newer non-import event must never displace the real media.imported source.
	f.exec(t, `INSERT INTO outbox_event(id,aggregate_type,aggregate_id,event_type,payload,seq,created_at) SELECT $1,aggregate_type,aggregate_id,'media.updated',payload,4,'2026-01-03' FROM outbox_event WHERE id=$2`, uuid.New(), newer)
	before := f.graph(t)
	c := f.candidates(t)
	if len(c) != 1 || c[0].SourceEventID != newer || c[0].FileID != f.file || c[0].UnitID != f.unit || c[0].MediaID != f.media || c[0].Status != ports.MediaScrapeMissing || c[0].RelPath != "Movie/source.mkv" {
		t.Fatalf("deduped candidate: %+v", c)
	}
	if f.graph(t) != before {
		t.Fatal("candidate GET wrote business data")
	}
	p := f.start(t, "latest-event")
	var source uuid.UUID
	if err := f.pool.QueryRow(t.Context(), `SELECT source_event_id FROM media_scrape_item WHERE batch_id=$1`, p.ID).Scan(&source); err != nil || source != newer {
		t.Fatalf("queued source=%s err=%v", source, err)
	}
	for name, sql := range map[string]string{
		"mapping_retired":       `UPDATE library_file_unit SET retired_at=clock_timestamp()`,
		"file_retired":          `UPDATE library_file SET retired_at=clock_timestamp()`,
		"unit_retired":          `UPDATE media_unit SET retired=true`,
		"fact_revoked":          `UPDATE unit_fact SET revoked_at=clock_timestamp()`,
		"not_imported":          `UPDATE unit_fact SET stage='confirmed'`,
		"event_wrong_kind":      `UPDATE outbox_event SET event_type='media.updated'`,
		"event_wrong_aggregate": `UPDATE outbox_event SET aggregate_id=gen_random_uuid()::text`,
		"event_wrong_file":      `UPDATE outbox_event SET payload=jsonb_set(payload,'{file_id}',to_jsonb(gen_random_uuid()))`,
		"offline_root":          `UPDATE library_root SET health='offline'`,
		"baseline_missing":      `UPDATE library_root SET baseline_completed=false`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(t.Context(), sql); err != nil {
				t.Fatal(err)
			}
			var n int
			if err = tx.QueryRow(t.Context(), `SELECT count(*) FROM media_scrape_candidate`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("invalid evidence candidates=%d err=%v", n, err)
			}
		})
	}
}

func TestScrapeAllSubjectJobsDetermineEligibility(t *testing.T) {
	for _, state := range []string{"queued", "running", "succeeded", "dead", "review_required"} {
		t.Run(state, func(t *testing.T) {
			f := newScrapeFixture(t)
			jobState := state
			if state == "review_required" {
				jobState = "dead"
			}
			id := f.job(t, jobState, "media-sidecar-rescan:previous:"+f.event.String(), "2026-01-03T00:00:00Z")
			if state == "review_required" {
				f.exec(t, `UPDATE job SET error='review_required' WHERE id=$1`, id)
			}
			want := map[string]ports.MediaScrapeStatus{"queued": ports.MediaScrapeQueued, "running": ports.MediaScrapeRunning, "succeeded": ports.MediaScrapeComplete, "dead": ports.MediaScrapeReview, "review_required": ports.MediaScrapeReview}[state]
			c := f.candidates(t)
			if len(c) != 1 || c[0].Status != want {
				t.Fatalf("status %+v want %s", c, want)
			}
			before := f.counts(t)
			p, _, err := f.repo.StartMediaScrape(t.Context(), f.actor, "explicit-check", uuid.New)
			if state == "succeeded" {
				if err != nil || p.Total != 1 || p.Queued != 1 || p.Running != 0 || f.counts(t) != [4]int{1, 2, 1, 1} {
					t.Fatalf("complete not explicitly eligible: %+v %v counts=%v", p, err, f.counts(t))
				}
			} else if !errors.Is(err, ports.ErrRejected) || f.counts(t) != before {
				t.Fatalf("blocked state queued: %+v %v", p, err)
			}
		})
	}
	t.Run("active_old_event_overrides_new_terminal", func(t *testing.T) {
		f := newScrapeFixture(t)
		f.job(t, "running", "media-sidecar:"+f.event.String(), "2026-01-01T00:00:00Z")
		f.job(t, "succeeded", "media-sidecar-rescan:new-terminal", "2026-01-04T00:00:00Z")
		f.eventAt(t, uuid.New(), 2, "2026-01-05T00:00:00Z")
		c := f.candidates(t)
		if len(c) != 1 || c[0].Status != ports.MediaScrapeRunning {
			t.Fatalf("older active job ignored: %+v", c)
		}
		if _, _, err := f.repo.StartMediaScrape(t.Context(), f.actor, "blocked", uuid.New); !errors.Is(err, ports.ErrRejected) {
			t.Fatal(err)
		}
	})
}

func TestScrapeConcurrentCommandsAndOriginalReceipt(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_key_%v", same), func(t *testing.T) {
			f := newScrapeFixture(t)
			type result struct {
				p      ports.MediaScrapeProgress
				reused bool
				err    error
			}
			gate := make(chan struct{})
			done := make(chan result, 2)
			for i := 0; i < 2; i++ {
				key := "race"
				if !same {
					key = fmt.Sprintf("race-%d", i)
				}
				go func() {
					<-gate
					p, r, e := f.repo.StartMediaScrape(t.Context(), f.actor, key, uuid.New)
					done <- result{p, r, e}
				}()
			}
			close(gate)
			a, b := <-done, <-done
			if same {
				if a.err != nil || b.err != nil || a.p.ID != b.p.ID || a.reused == b.reused {
					t.Fatalf("same-key results: %+v %+v", a, b)
				}
			} else {
				winner, loser := a, b
				if winner.err != nil {
					winner, loser = b, a
				}
				if winner.err != nil || !errors.Is(loser.err, ports.ErrRejected) {
					t.Fatalf("different-key results: %+v %+v", a, b)
				}
			}
			if f.counts(t) != [4]int{1, 1, 1, 1} {
				t.Fatal("double enqueue", f.counts(t))
			}
			winner := a
			if winner.err != nil {
				winner = b
			}
			f.exec(t, `UPDATE job SET state='succeeded'`)
			f.eventAt(t, uuid.New(), 2, "2026-02-01T00:00:00Z")
			f.exec(t, `UPDATE library_file_unit SET retired_at=clock_timestamp()`)
			// Receipt lookup must happen before fresh candidates, even when none remain.
			p, reused, err := f.repo.StartMediaScrape(t.Context(), f.actor, winner.p.CommandKey, func() uuid.UUID { t.Error("receipt replay allocated an ID"); return uuid.New() })
			if err != nil || !reused || p.ID != winner.p.ID || p.Succeeded != 1 || f.counts(t) != [4]int{1, 1, 1, 1} {
				t.Fatalf("original receipt lost: %+v %v %v", p, reused, err)
			}
		})
	}
}

func TestScrapeReceiptsAccountScopeProgressAndReadOnly(t *testing.T) {
	f := newScrapeFixture(t)
	if p, e := f.repo.ReadMediaScrapeBatch(t.Context(), f.actor, ""); e != nil || p != nil {
		t.Fatalf("empty latest: %+v %v", p, e)
	}
	first := f.start(t, "shared-key")
	other := f.admin(t)
	before := f.graph(t)
	if p, e := f.repo.ReadMediaScrapeBatch(t.Context(), other, "shared-key"); e != nil || p != nil {
		t.Fatalf("cross-account receipt leaked: %+v %v", p, e)
	}
	if p, e := f.repo.ReadMediaScrapeBatch(t.Context(), f.actor, "missing-key"); e != nil || p != nil {
		t.Fatal(p, e)
	}
	if p, e := f.repo.ReadMediaScrapeBatch(t.Context(), f.actor, ""); e != nil || p == nil || p.ID != first.ID || p.Queued != 1 || p.Running != 0 {
		t.Fatal(p, e)
	}
	if f.graph(t) != before {
		t.Fatal("GET wrote business data")
	}
	for _, state := range []string{"running", "succeeded", "dead"} {
		f.exec(t, `UPDATE job SET state=$1`, state)
		p, e := f.repo.ReadMediaScrapeBatch(t.Context(), f.actor, "shared-key")
		if e != nil || p == nil {
			t.Fatal(p, e)
		}
		if (state == "running" && (p.Running != 1 || p.Queued != 0)) || (state == "succeeded" && p.Succeeded != 1) || (state == "dead" && p.Review != 1) {
			t.Fatalf("progress state %s: %+v", state, p)
		}
	}
	f.exec(t, `UPDATE job SET state='succeeded'`)
	f.actor = other
	second := f.start(t, "shared-key")
	if second.ID == first.ID || f.counts(t) != [4]int{2, 2, 2, 2} {
		t.Fatal("account keys collided", second, f.counts(t))
	}
	p, e := f.repo.ReadMediaScrapeBatch(t.Context(), other, "shared-key")
	if e != nil || p == nil || p.ID != second.ID {
		t.Fatal(p, e)
	}
}

func TestScrapeAuditTailFailureRollsBackEverything(t *testing.T) {
	f := newScrapeFixture(t)
	f.exec(t, `CREATE FUNCTION reject_scrape_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='media.scrape.queued' THEN RAISE EXCEPTION 'injected scrape audit tail failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_scrape_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_scrape_audit()`)
	before := f.graph(t)
	_, _, err := f.repo.StartMediaScrape(t.Context(), f.actor, "tail-failure", uuid.New)
	if err == nil || !strings.Contains(err.Error(), "injected scrape audit tail failure") {
		t.Fatalf("injection not reached: %v", err)
	}
	if f.graph(t) != before || f.counts(t) != [4]int{} {
		t.Fatal("partial batch survived audit failure", f.counts(t))
	}
	f.exec(t, `DROP TRIGGER reject_scrape_audit ON audit_log; DROP FUNCTION reject_scrape_audit()`)
	f.start(t, "tail-failure")
	if f.counts(t) != [4]int{1, 1, 1, 1} {
		t.Fatal(f.counts(t))
	}
}

func TestScrapeCurrentAuthorityAndRestoreIsolation(t *testing.T) {
	f := newScrapeFixture(t)
	f.start(t, "receipt")
	for _, mode := range []string{"anonymous", "member", "token", "missing_user", "inactive", "revoked_role", "restore"} {
		t.Run(mode, func(t *testing.T) {
			a := *f.actor
			actor := &a
			want := ports.ErrForbidden
			switch mode {
			case "anonymous":
				actor = nil
				want = ports.ErrUnauthorized
			case "member":
				a.Role = ports.RoleMember
			case "token":
				a.Scopes = []string{"read"}
			case "missing_user":
				a.ID = uuid.NewString()
			case "inactive":
				f.exec(t, `UPDATE app_user SET is_active=false`)
			case "revoked_role":
				f.exec(t, `UPDATE app_user SET role='member'`)
			case "restore":
				f.exec(t, `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid()`)
				want = ports.ErrUnavailable
			}
			defer f.exec(t, `UPDATE app_user SET role='admin',is_active=true; UPDATE restore_isolation SET isolated=false,restore_id=NULL`)
			before := f.graph(t)
			_, err := f.repo.ListMediaScrapeCandidates(t.Context(), actor)
			if !errors.Is(err, want) {
				t.Fatalf("list: %v want %v", err, want)
			}
			_, err = f.repo.ReadMediaScrapeBatch(t.Context(), actor, "receipt")
			if !errors.Is(err, want) {
				t.Fatalf("read: %v want %v", err, want)
			}
			_, _, err = f.repo.StartMediaScrape(t.Context(), actor, "receipt", uuid.New)
			if !errors.Is(err, want) {
				t.Fatalf("start: %v want %v", err, want)
			}
			if f.graph(t) != before {
				t.Fatal("unauthorized operation wrote")
			}
		})
	}
}

// Wait for a real server lock wait, rather than assuming a goroutine has reached SQL.
func (f scrapeFixture) waitForLock(t *testing.T, kind string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE a.datname=current_database() AND NOT l.granted AND l.locktype=$1)`, kind).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("repository did not reach lock wait", kind)
		case <-tick.C:
		}
	}
}
func TestScrapeRevocationAfterSubmissionLockAndBeforeRead(t *testing.T) {
	for _, op := range []string{"start", "list", "read"} {
		t.Run(op, func(t *testing.T) {
			f := newScrapeFixture(t)
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if op == "start" {
				_, err = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(734077)`)
			} else {
				_, err = tx.Exec(t.Context(), `UPDATE app_user SET role='member' WHERE id=$1`, f.actor.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var e error
				switch op {
				case "start":
					_, _, e = f.repo.StartMediaScrape(ctx, f.actor, "revoked", uuid.New)
				case "list":
					_, e = f.repo.ListMediaScrapeCandidates(ctx, f.actor)
				case "read":
					_, e = f.repo.ReadMediaScrapeBatch(ctx, f.actor, "")
				}
				done <- e
			}()
			// Cleanup also joins the goroutine if an assertion fails while it is blocked.
			defer func() {
				cancel()
				_ = tx.Rollback(context.Background())
				select {
				case <-done:
				case <-time.After(16 * time.Second):
					t.Error("repository goroutine did not stop")
				}
			}()
			kind := "transactionid"
			if op == "start" {
				kind = "advisory"
			}
			f.waitForLock(t, kind)
			if op == "start" {
				f.exec(t, `UPDATE app_user SET role='member' WHERE id=$1`, f.actor.ID)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			err = <-done
			// Keep the deferred join nonblocking after consuming the result.
			done <- err
			if !errors.Is(err, ports.ErrForbidden) || f.counts(t) != [4]int{} {
				t.Fatalf("post-lock revocation failed: %v counts=%v", err, f.counts(t))
			}
		})
	}
}

func TestScrapeSchema94RoundTripPreservesImportedHistory(t *testing.T) {
	f := newScrapeFixture(t)
	if postgres.CurrentMigrationVersion != 94 {
		t.Skip("roundtrip is specific to migration94")
	}
	before := f.graph(t)
	dsn := f.pool.Config().ConnConfig.ConnString()
	v, err := postgres.Rollback(t.Context(), dsn, "../../../migrations")
	if err != nil || v != 93 {
		t.Fatalf("rollback94: %d %v", v, err)
	}
	v, err = postgres.Migrate(t.Context(), dsn, "../../../migrations")
	if err != nil || v != postgres.CurrentMigrationVersion {
		t.Fatalf("reapply94: %d %v", v, err)
	}
	if f.graph(t) != before {
		t.Fatal("migration94 roundtrip changed imported history")
	}
	if c := f.candidates(t); len(c) != 1 || c[0].SourceEventID != f.event {
		t.Fatal(c)
	}
	f.start(t, "preserved-history")
	before = f.graph(t)
	if _, err = postgres.Rollback(t.Context(), dsn, "../../../migrations"); err == nil {
		t.Fatal("downgrade dropped original-key receipts")
	}
	if err = postgres.CheckSchemaVersion(t.Context(), f.pool, postgres.CurrentMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if f.graph(t) != before {
		t.Fatal("rejected downgrade changed command history")
	}
}
