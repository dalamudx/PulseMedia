package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func dispatchFixture(t *testing.T) (*preparedUpgradeFixture, ports.MovieUpgradeSendDispatchSession) {
	t.Helper()
	ctx := context.Background()
	f := armFixture(t)
	if v, e := postgres.Migrate(ctx, f.dsn, dispatchMigrationDir(t)); e != nil || v != 68 {
		t.Fatal(v, e)
	}
	r, e := postgres.NewMovieUpgradeSendDispatchRepository(f.preparation)
	if e != nil {
		t.Fatal(e)
	}
	s, e := r.OpenMovieUpgradeSendDispatchSession(ctx, f.operation)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(context.Background()); e != nil {
			t.Error(e)
		}
	})
	if _, e = s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "dispatch-arm"); e != nil {
		t.Fatal(e)
	}
	return f, s
}
func noDispatch(t *testing.T, f *preparedUpgradeFixture) {
	t.Helper()
	var n int
	e := f.p.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM movie_upgrade_send_dispatch)+(SELECT count(*) FROM audit_log WHERE action='upgrade.dispatch')+(SELECT count(*) FROM outbox_event WHERE event_type='upgrade.dispatch')+(SELECT count(*) FROM idempotency_record WHERE idempotency_key LIKE 'upgrade.dispatch:%')`).Scan(&n)
	if e != nil || n != 0 {
		t.Fatalf("partial dispatch: %d %v", n, e)
	}
}
func TestMovieUpgradeDispatchPG(t *testing.T) {
	f, s := dispatchFixture(t)
	ctx := context.Background()
	old, graph := f.oldState(t), returnGraph(t, f)
	receipt, e := s.DispatchMovieUpgradeSend(ctx, f.actor, f.proof, "dispatch")
	if e != nil {
		t.Fatal(e)
	}
	if receipt.OperationID != f.operation || receipt.DispatchID == uuid.Nil || receipt.TaskID == uuid.Nil || receipt.DispatchedAt.IsZero() || receipt.ExecutionAllowed || receipt.Status != "dispatch_recorded_not_sent" {
		t.Fatalf("bad receipt: %+v", receipt)
	}
	raw, e := json.Marshal(receipt)
	if e != nil {
		t.Fatal(e)
	}
	for _, private := range []string{"arm_sha256", "raw_sha256", "token", "nonce", "proof"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private binding escaped")
		}
	}
	if _, e = s.DispatchMovieUpgradeSend(ctx, f.actor, f.proof, "dispatch"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatalf("repeated dispatch: %v", e)
	}
	if graph != returnGraph(t, f) || old != f.oldState(t) {
		t.Fatal("dispatch changed old or protected state")
	}
	if e = s.Close(ctx); e != nil {
		t.Fatal(e)
	}
	r, e := postgres.NewMovieUpgradeSendDispatchRepository(f.preparation)
	if e != nil {
		t.Fatal(e)
	}
	second, e := r.OpenMovieUpgradeSendDispatchSession(ctx, f.operation)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := second.Close(context.Background()); e != nil {
			t.Error(e)
		}
	}()
	if _, e = second.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "again"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatalf("new session armed: %v", e)
	}
	if _, e = second.DispatchMovieUpgradeSend(ctx, f.actor, f.proof, "again"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatalf("new session dispatched: %v", e)
	}
	var retained bool
	e = f.p.QueryRow(ctx, `SELECT count(*)=1 FROM movie_upgrade_send_dispatch WHERE operation_id=$1`, f.operation).Scan(&retained)
	if e != nil || !retained {
		t.Fatal("dispatch not retained", e)
	}
	for _, stmt := range []string{"UPDATE movie_upgrade_send_dispatch SET dispatched_at=dispatched_at", "DELETE FROM movie_upgrade_send_dispatch", "TRUNCATE movie_upgrade_send_dispatch"} {
		if _, e = f.p.Exec(ctx, stmt); e == nil {
			t.Fatal("immutable history changed", stmt)
		}
	}
	if _, e := postgres.Rollback(ctx, f.dsn, dispatchMigrationDir(t)); e == nil {
		t.Fatal("dispatch history downgraded")
	}
	if e := postgres.CheckSchemaVersion(ctx, f.p, 68); e != nil {
		t.Fatal(e)
	}
}
func TestMovieUpgradeDispatchConcurrentPG(t *testing.T) {
	f, s := dispatchFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.DispatchMovieUpgradeSend(context.Background(), f.actor, f.proof, "concurrent")
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	success, blocked := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
			blocked++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || blocked != 7 {
		t.Fatal("not one-shot", success, blocked)
	}
	var n int
	if e := f.p.QueryRow(context.Background(), `SELECT count(*) FROM movie_upgrade_send_dispatch`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
func TestMovieUpgradeDispatchTailPG(t *testing.T) {
	for _, kind := range []string{"dispatch", "fact", "receipt", "deferred_fact"} {
		t.Run(kind, func(t *testing.T) {
			f, s := dispatchFixture(t)
			graph, old := returnGraph(t, f), f.oldState(t)
			switch kind {
			case "dispatch":
				f.exec(t, `CREATE FUNCTION test_dispatch_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN NEW.token:=gen_random_uuid();RETURN NEW;END$$;CREATE TRIGGER test_dispatch_tail BEFORE INSERT ON movie_upgrade_send_dispatch FOR EACH ROW EXECUTE FUNCTION test_dispatch_tail()`)
			case "fact":
				f.exec(t, `CREATE FUNCTION test_dispatch_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='upgrade.dispatch' THEN UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second' WHERE id=(SELECT old_fact_id FROM movie_upgrade_authority WHERE operation_id=(NEW.payload->>'operation_id')::uuid);END IF;RETURN NEW;END$$;CREATE TRIGGER test_dispatch_tail AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION test_dispatch_tail()`)
			case "receipt":
				f.exec(t, `CREATE FUNCTION test_dispatch_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key LIKE 'upgrade.dispatch:%' THEN RAISE EXCEPTION 'dispatch receipt tail';END IF;RETURN NEW;END$$;CREATE TRIGGER test_dispatch_tail BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION test_dispatch_tail()`)
			case "deferred_fact":
				f.exec(t, `CREATE FUNCTION test_dispatch_tail() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key LIKE 'upgrade.dispatch:%' THEN UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second' WHERE id=(SELECT old_fact_id FROM movie_upgrade_authority WHERE operation_id=NEW.resource_id::uuid);END IF;RETURN NEW;END$$;CREATE CONSTRAINT TRIGGER test_dispatch_tail AFTER INSERT ON idempotency_record DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION test_dispatch_tail()`)
			}
			if _, e := s.DispatchMovieUpgradeSend(context.Background(), f.actor, f.proof, "tail"); e == nil {
				t.Fatal("tail accepted")
			}
			noDispatch(t, f)
			if graph != returnGraph(t, f) || old != f.oldState(t) {
				t.Fatal("tail left changed protections")
			}
		})
	}
}

type wrappedDispatchProof struct {
	ports.MovieUpgradeMetainfoProof
}

func TestMovieUpgradeDispatchChangedPG(t *testing.T) {
	for _, kind := range []string{"client", "fact", "account", "wrapped_proof", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			f, s := dispatchFixture(t)
			ctx := context.Background()
			proof := ports.MovieUpgradeMetainfoProof(f.proof)
			switch kind {
			case "client":
				f.exec(t, `UPDATE download_client SET url=url||'/changed' WHERE id=$1`, f.client)
			case "fact":
				f.exec(t, `UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.fact)
			case "account":
				f.exec(t, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID)
			case "wrapped_proof":
				proof = wrappedDispatchProof{proof}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			graph, old := returnGraph(t, f), f.oldState(t)
			if _, e := s.DispatchMovieUpgradeSend(ctx, f.actor, proof, "changed"); e == nil {
				t.Fatal("changed input accepted")
			}
			noDispatch(t, f)
			if graph != returnGraph(t, f) || old != f.oldState(t) {
				t.Fatal("changed input released protection")
			}
		})
	}
}
func TestMovieUpgradeDispatchDisabledPG(t *testing.T) {
	f := armFixture(t)
	ctx := context.Background()
	if v, e := postgres.Migrate(ctx, f.dsn, dispatchMigrationDir(t)); e != nil || v != 68 {
		t.Fatal(v, e)
	}
	s := armSession(t, f)
	if _, e := s.ArmMovieUpgradeSend(ctx, f.actor, uuid.New(), f.proof, "default-arm"); e != nil {
		t.Fatal(e)
	}
	explicit, ok := s.(ports.MovieUpgradeSendDispatchSession)
	if !ok {
		t.Fatal("missing dispatch implementation")
	}
	if _, e := explicit.DispatchMovieUpgradeSend(ctx, f.actor, f.proof, "default"); !errors.Is(e, ports.ErrMovieUpgradeSendObservation) {
		t.Fatal("default gained dispatch", e)
	}
	noDispatch(t, f)
}
