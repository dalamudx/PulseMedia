package postgres_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func r3Repository(t *testing.T, n *nativeUpgradeFixture, v *r3Verifier) ports.MovieUpgradeObservationRepository {
	t.Helper()
	r, e := postgres.NewMovieUpgradeObservationRepository(n.preparation, v)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestMovieUpgradeR3HistoricalRecovery(t *testing.T) {
	n := r3Native(t, "ambiguous", true)
	ctx := context.Background()
	v := &r3Verifier{}
	r := r3Repository(t, n, v)
	s := r3Open(t, n, v)
	owner := uuid.New()
	admitted, e := s.AdmitMovieUpgradeObservation(ctx, n.actor, owner, "admit-history")
	if e != nil {
		t.Fatal(e)
	}
	v.proof = &r3Proof{outcome: "unknown", at: time.Now()}
	result, e := s.RecordMovieUpgradeObservation(ctx, n.actor, "result-history", v.proof)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(ctx); e != nil {
		t.Fatal(e)
	}
	before := r3Protected(t, n)
	n.exec(t, `UPDATE download_client SET url='http://rotated.invalid' WHERE id=$1`, n.client)
	recovered, e := r.ReadMovieUpgradeObservationAdmission(ctx, n.actor, n.operation, owner, "admit-history")
	if e != nil || !reflect.DeepEqual(admitted, recovered) {
		t.Fatal("admission recovery changed receipt", e)
	}
	got, e := r.ReadMovieUpgradeObservationResult(ctx, n.actor, n.operation, "result-history")
	if e != nil || !reflect.DeepEqual(result, got) {
		t.Fatal("result recovery changed receipt", e)
	}
	if _, e = r.ReadMovieUpgradeObservationAdmission(ctx, n.actor, n.operation, uuid.New(), "admit-history"); e == nil {
		t.Fatal("wrong owner recovered receipt")
	}
	if _, e = r.ReadMovieUpgradeObservationResult(ctx, n.actor, n.operation, "wrong-key"); e == nil {
		t.Fatal("wrong key recovered receipt")
	}
	token := *n.actor
	token.Scopes = []string{"read"}
	if _, e = r.ReadMovieUpgradeObservationResult(ctx, &token, n.operation, "result-history"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("API credential recovered command receipt", e)
	}
	if before != r3Protected(t, n) {
		t.Fatal("history changed protected graph")
	}
	var count int
	if e = n.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_observation_permit WHERE operation_id=$1`, n.operation).Scan(&count); e != nil || count != 1 {
		t.Fatal("history created permit", e)
	}
}
func TestMovieUpgradeR3FinalLeaseAndExpiredHistory(t *testing.T) {
	n := r3Native(t, "ambiguous", true)
	older := r3Native(t, "ambiguous", true)
	ctx := context.Background()
	v := &r3Verifier{}
	r := r3Repository(t, n, v)
	s := r3Open(t, n, v)
	owner := uuid.New()
	admitted, e := s.AdmitMovieUpgradeObservation(ctx, n.actor, owner, "expiry-admit")
	if e != nil {
		t.Fatal(e)
	}
	v.proof = &r3Proof{outcome: "unknown", at: time.Now()}
	result, e := s.RecordMovieUpgradeObservation(ctx, n.actor, "expiry-result", v.proof)
	if e != nil {
		t.Fatal(e)
	}
	before := r3Protected(t, n)
	var body string
	if e = n.p.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE oid='pulse_assert_upgrade_preparation'::regproc`).Scan(&body); e != nil {
		t.Fatal(e)
	}
	n.exec(t, `CREATE FUNCTION test_r3_original_assert(op uuid) RETURNS void LANGUAGE plpgsql AS $body$`+body+`$body$;CREATE SEQUENCE test_r3_expiry_hit;CREATE OR REPLACE FUNCTION pulse_assert_upgrade_preparation(op uuid) RETURNS void LANGUAGE plpgsql AS $$DECLARE until_at timestamptz;BEGIN PERFORM test_r3_original_assert(op);SELECT lease_until INTO until_at FROM movie_upgrade_observation WHERE operation_id=op;IF FOUND THEN PERFORM nextval('test_r3_expiry_hit');PERFORM pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM(until_at-clock_timestamp())))+0.03);END IF;END$$`)
	if _, e = n.p.Exec(ctx, `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM(admitted_at+interval '50 seconds'-clock_timestamp())))) FROM movie_upgrade_observation WHERE operation_id=$1`, n.operation); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RecordMovieUpgradeObservation(ctx, n.actor, "expiry-result", v.proof); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal("late execution accepted or misclassified", e)
	}
	var expired, hit bool
	if e = n.p.QueryRow(ctx, `SELECT lease_until<=clock_timestamp(),(SELECT is_called FROM test_r3_expiry_hit) FROM movie_upgrade_observation WHERE operation_id=$1`, n.operation).Scan(&expired, &hit); e != nil || !expired || !hit {
		t.Fatal("test never crossed real lease", e)
	}
	got, e := r.ReadMovieUpgradeObservationResult(ctx, n.actor, n.operation, "expiry-result")
	if e != nil || !reflect.DeepEqual(result, got) {
		t.Fatal("expired history lost", e)
	}
	a, e := r.ReadMovieUpgradeObservationAdmission(ctx, n.actor, n.operation, owner, "expiry-admit")
	if e != nil || !reflect.DeepEqual(admitted, a) {
		t.Fatal("expired admission history lost", e)
	}
	if before != r3Protected(t, n) {
		t.Fatal("expired execution changed original graph")
	}
	var oldExpired bool
	if e = older.p.QueryRow(ctx, `SELECT lease_until<=clock_timestamp() FROM movie_upgrade_send_authorization WHERE operation_id=$1`, older.operation).Scan(&oldExpired); e != nil || !oldExpired {
		t.Fatal("original send lease did not expire", e)
	}
	next := r3Open(t, older, &r3Verifier{})
	if _, e = next.AdmitMovieUpgradeObservation(ctx, older.actor, uuid.New(), "after-original-expiry"); e != nil {
		t.Fatal("real UNKNOWN return incorrectly needs live original send lease", e)
	}
}
func TestMovieUpgradeR3RevokeWait(t *testing.T) {
	for _, phase := range []string{"admit", "result", "history"} {
		t.Run(phase, func(t *testing.T) {
			n := r3Native(t, "ambiguous", true)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			v := &r3Verifier{}
			r := r3Repository(t, n, v)
			s := r3Open(t, n, v)
			owner := uuid.New()
			if phase != "admit" {
				if _, e := s.AdmitMovieUpgradeObservation(ctx, n.actor, owner, "wait-admit"); e != nil {
					t.Fatal(e)
				}
			}
			tx, e := n.p.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer rollbackFrozenUpgradeTest(t, tx)
			if _, e = tx.Exec(ctx, `SELECT id FROM app_user WHERE id=$1 FOR UPDATE`, n.actor.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			v.proof = &r3Proof{outcome: "unknown", at: time.Now()}
			go func() {
				var e error
				switch phase {
				case "admit":
					_, e = s.AdmitMovieUpgradeObservation(ctx, n.actor, owner, "wait-admit")
				case "result":
					_, e = s.RecordMovieUpgradeObservation(ctx, n.actor, "wait-result", v.proof)
				default:
					_, e = r.ReadMovieUpgradeObservationAdmission(ctx, n.actor, n.operation, owner, "wait-admit")
				}
				done <- e
			}()
			drained := false
			defer func() {
				cancel()
				if !drained {
					<-done
				}
			}()
			waitPreparationBlock(t, n.p, tx.Conn().PgConn().PID())
			if _, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, n.actor.ID); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			drained = true
			if !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("wait revocation bypassed", e)
			}
			var results int
			if e = n.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_observation_result`).Scan(&results); e != nil || results != 0 {
				t.Fatal("revoked command persisted result", e)
			}
		})
	}
}
func TestMovieUpgradeR3BackupAndBusinessRole(t *testing.T) {
	n := r3Native(t, "ambiguous", true)
	ctx := context.Background()
	_, dml := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, n.dsn))
	pool, e := pgxpool.New(ctx, dml)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	store, e := postgres.NewUpgradePreparationStore(pool, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	v := &r3Verifier{}
	r, e := postgres.NewMovieUpgradeObservationRepository(store, v)
	if e != nil {
		t.Fatal(e)
	}
	s, e := r.OpenMovieUpgradeObservationSession(ctx, n.operation)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := s.Close(context.Background()); e != nil {
			t.Error(e)
		}
	}()
	a, e := s.AdmitMovieUpgradeObservation(ctx, n.actor, uuid.New(), "business-admit")
	if e != nil {
		t.Fatal(e)
	}
	v.proof = &r3Proof{outcome: "absent", at: time.Now()}
	if _, e = s.RecordMovieUpgradeObservation(ctx, n.actor, "business-result", v.proof); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`ALTER TABLE movie_upgrade_observation ADD COLUMN denied int`, `DELETE FROM movie_upgrade_observation_result`, `UPDATE movie_upgrade_observation SET token=gen_random_uuid()`, `TRUNCATE movie_upgrade_observation_permit`} {
		if _, e = pool.Exec(ctx, q); e == nil {
			t.Fatal("DML exceeded rights", q)
		}
	}
	dump := filepath.Join(t.TempDir(), "r3.dump")
	frozenPGCommand(t, ctx, "pg_dump", n.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	p, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = postgres.CheckSchemaVersion(ctx, p, 69); e != nil {
		t.Fatal(e)
	}
	var retained, leak bool
	e = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_observation_result WHERE observation_id=$1 AND outcome='absent' AND disposition='manual_review'),EXISTS(SELECT 1 FROM movie_upgrade_observation o WHERE to_jsonb(o)::text LIKE '%passkey/abcd%')`, a.ObservationID).Scan(&retained, &leak)
	if e != nil || !retained || leak {
		t.Fatal("restore history/leak", e)
	}
	for _, q := range []string{`DELETE FROM movie_upgrade_observation_result`, `TRUNCATE movie_upgrade_observation`, `UPDATE download_task SET state='tracking'`} {
		if _, e = p.Exec(ctx, q); e == nil {
			t.Fatal("restored graph mutable", q)
		}
	}
	if _, e = postgres.Rollback(ctx, restored, observation69MigrationDir(t)); e == nil || !strings.Contains(e.Error(), "upgrade observation history prevents downgrade") {
		t.Fatal("expected retained observation guard", e)
	}
}
func TestMovieUpgradeR3MigrationRoundTrip(t *testing.T) {
	n := r3Native(t, "ambiguous", true)
	ctx := context.Background()
	dir := observation69MigrationDir(t)
	if v, e := postgres.Rollback(ctx, n.dsn, dir); e != nil || v != 68 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Migrate(ctx, n.dsn, dir); e != nil || v != 69 {
		t.Fatal(v, e)
	}
}
func TestMovieUpgradeR3DownConcurrent(t *testing.T) {
	n := r3Native(t, "ambiguous", true)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	s := r3Open(t, n, &r3Verifier{})
	holder, e := n.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, holder)
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_xact_lock(690,1)`); e != nil {
		t.Fatal(e)
	}
	n.exec(t, `CREATE FUNCTION test_r3_down_wait() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='upgrade.observation-admit' THEN PERFORM pg_advisory_xact_lock(690,1);END IF;RETURN NEW;END$$;CREATE TRIGGER test_r3_down_wait AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION test_r3_down_wait()`)
	sent, down := make(chan error, 1), make(chan error, 1)
	drained, started, downDrained := false, false, false
	defer func() {
		cancel()
		if !drained {
			<-sent
		}
		if started && !downDrained {
			<-down
		}
	}()
	go func() { _, e := s.AdmitMovieUpgradeObservation(ctx, n.actor, uuid.New(), "concurrent-down"); sent <- e }()
	waitPreparationBlock(t, n.p, holder.Conn().PgConn().PID())
	var pid uint32
	if e = n.p.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1::INTEGER=ANY(pg_blocking_pids(pid))`, holder.Conn().PgConn().PID()).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	started = true
	go func() {
		_, e := postgres.Rollback(ctx, n.dsn, observation69MigrationDir(t))
		down <- e
	}()
	waitPreparationBlock(t, n.p, pid)
	if e = holder.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-sent
	drained = true
	if e != nil {
		t.Fatal(e)
	}
	e = <-down
	downDrained = true
	if e == nil || !strings.Contains(e.Error(), "upgrade observation history prevents downgrade") {
		t.Fatal("expected concurrent retained-history guard", e)
	}
	if e = postgres.CheckSchemaVersion(ctx, n.p, 69); e != nil {
		t.Fatal(e)
	}
}
