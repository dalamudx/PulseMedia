package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"sync"
	"testing"
	"time"
)

func confirmationFixture(t *testing.T) (*organizationFixture, *postgres.MediaConfirmationStore, *ports.MediaConfirmationInput) {
	t.Helper()
	f := organizationSetup(t)
	ctx := context.Background()
	file := uuid.New()
	if _, e := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: file, RootID: f.root, RelPath: "confirm.mkv", SizeBytes: 18, MtimeNs: 1, LastMutationSeq: 1}); e != nil {
		t.Fatal(e)
	}
	if e := f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file, UnitID: f.unit}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: f.unit, Stage: "imported", RootID: pgtype.UUID{Bytes: f.root, Valid: true}, SourceFileID: pgtype.UUID{Bytes: file, Valid: true}, QualityProfile: []byte(`{"legacy":"unchanged"}`)}); e != nil {
		t.Fatal(e)
	}
	server := uuid.New()
	if _, e := f.q.CreateMediaServer(ctx, db.CreateMediaServerParams{ID: server, Name: "native", ServerType: "jellyfin", Url: "https://server.invalid", ApiKeyEncrypted: []byte("encrypted-fixture"), PathMapping: []byte(fmt.Sprintf(`{%q:"/movies"}`, f.target))}); e != nil {
		t.Fatal(e)
	}
	store := postgres.NewMediaConfirmationRepository(f.pool, f.gen)
	in, e := store.LoadMediaConfirmation(ctx, server, file, f.unit)
	if e != nil {
		t.Fatal(e)
	}
	return f, store, in
}
func confirmationState(t *testing.T, f *organizationFixture, in *ports.MediaConfirmationInput) string {
	t.Helper()
	var v string
	e := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_object('root_version',(SELECT mutation_seq FROM library_root WHERE id=$1),'unit_version',(SELECT facts_version FROM media_unit WHERE id=$2),'facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x WHERE source_file_id=$3),'audit',(SELECT count(*) FROM audit_log WHERE action='media.confirmed'),'events',(SELECT count(*) FROM outbox_event WHERE event_type='media.confirmed'))::text`, in.RootID, in.UnitID, in.FileID).Scan(&v)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestMediaConfirmationEightWayWritesOnce(t *testing.T) {
	f, s, in := confirmationFixture(t)
	before, e := f.q.GetLibraryRootByID(context.Background(), in.RootID)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan *ports.MediaConfirmationFact, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.CommitMediaConfirmation(context.Background(), *in, "native-item")
			if e != nil {
				errs <- e
				return
			}
			results <- v
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	for e := range errs {
		t.Fatal(e)
	}
	var id uuid.UUID
	for r := range results {
		if id == uuid.Nil {
			id = r.ID
		}
		if r.ID != id || r.SourceFileID != in.FileID || r.Stage != "confirmed" {
			t.Fatal("replay changed original fact")
		}
	}
	after, e := f.q.GetLibraryRootByID(context.Background(), in.RootID)
	if e != nil || after.MutationSeq != before.MutationSeq+1 {
		t.Fatal("root version advanced more than once", e)
	}
	var facts, audit, events int
	e = f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM unit_fact WHERE source_file_id=$1 AND stage='confirmed'),(SELECT count(*) FROM audit_log WHERE action='media.confirmed'),(SELECT count(*) FROM outbox_event WHERE event_type='media.confirmed')`, in.FileID).Scan(&facts, &audit, &events)
	if e != nil || facts != 1 || audit != 1 || events != 1 {
		t.Fatal("duplicate confirmation outputs", facts, audit, events, e)
	}
	snapshot := confirmationState(t, f, in)
	fresh, e := s.LoadMediaConfirmation(context.Background(), in.Server.ID, in.FileID, in.UnitID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CommitMediaConfirmation(context.Background(), *fresh, "native-item"); e != nil {
		t.Fatal(e)
	}
	if confirmationState(t, f, in) != snapshot {
		t.Fatal("normal replay changed state")
	}
}
func TestMediaConfirmationRejectsChangedInputs(t *testing.T) {
	cases := []struct {
		name, sql string
		target    func(*ports.MediaConfirmationInput) uuid.UUID
	}{{"server-url", `UPDATE media_server SET url='https://new.invalid' WHERE id=$1`, func(in *ports.MediaConfirmationInput) uuid.UUID { return in.Server.ID }}, {"server-key", `UPDATE media_server SET api_key_encrypted='new'::bytea WHERE id=$1`, func(in *ports.MediaConfirmationInput) uuid.UUID { return in.Server.ID }}, {"server-map", `UPDATE media_server SET path_mapping='{}'::jsonb WHERE id=$1`, func(in *ports.MediaConfirmationInput) uuid.UUID { return in.Server.ID }}, {"server-offline", `UPDATE media_server SET health='offline' WHERE id=$1`, func(in *ports.MediaConfirmationInput) uuid.UUID { return in.Server.ID }}, {"file-size", `UPDATE library_file SET size_bytes=19 WHERE id=$1`, func(in *ports.MediaConfirmationInput) uuid.UUID { return in.FileID }}, {"file-fingerprint", `UPDATE library_file SET file_fingerprint='changed' WHERE id=$1`, func(in *ports.MediaConfirmationInput) uuid.UUID { return in.FileID }}, {"file-retired", `UPDATE library_file SET retired_at=clock_timestamp() WHERE id=$1`, func(in *ports.MediaConfirmationInput) uuid.UUID { return in.FileID }}, {"root-path", `UPDATE library_root SET path='/replacement' WHERE id=$1`, func(in *ports.MediaConfirmationInput) uuid.UUID { return in.RootID }}, {"unit-retired", `UPDATE media_unit SET retired=true WHERE id=$1`, func(in *ports.MediaConfirmationInput) uuid.UUID { return in.UnitID }}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s, in := confirmationFixture(t)
			if _, e := f.pool.Exec(context.Background(), c.sql, c.target(in)); e != nil {
				t.Fatal(e)
			}
			before := confirmationState(t, f, in)
			if _, e := s.CommitMediaConfirmation(context.Background(), *in, "native-item"); e == nil {
				t.Fatal("stale response accepted")
			}
			if confirmationState(t, f, in) != before {
				t.Fatal("stale response mutated imported state")
			}
		})
	}
}
func TestMediaConfirmationTailFailureRollsBack(t *testing.T) {
	for _, c := range []struct{ name, table, field string }{{"audit", "audit_log", "action"}, {"event", "outbox_event", "event_type"}} {
		t.Run(c.name, func(t *testing.T) {
			f, s, in := confirmationFixture(t)
			ctx := context.Background()
			before := confirmationState(t, f, in)
			sql := fmt.Sprintf(`CREATE FUNCTION fail_confirm() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.%s='media.confirmed' THEN RAISE EXCEPTION 'confirmation-tail'; END IF; RETURN NEW; END $$;CREATE TRIGGER fail_confirm BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION fail_confirm()`, c.field, c.table)
			if _, e := f.pool.Exec(ctx, sql); e != nil {
				t.Fatal(e)
			}
			if _, e := s.CommitMediaConfirmation(ctx, *in, "native-item"); e == nil {
				t.Fatal("fault accepted")
			}
			if confirmationState(t, f, in) != before {
				t.Fatal("tail failure left outputs")
			}
			if _, e := f.pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER fail_confirm ON %s;DROP FUNCTION fail_confirm()`, c.table)); e != nil {
				t.Fatal(e)
			}
			if _, e := s.CommitMediaConfirmation(ctx, *in, "native-item"); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestMediaConfirmationRechecksAfterRootLock(t *testing.T) {
	f, s, in := confirmationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(ctx, `SELECT id FROM library_root WHERE id=$1 FOR UPDATE`, in.RootID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := s.CommitMediaConfirmation(ctx, *in, "native-item"); done <- e }()
	joined := false
	defer func() {
		_ = tx.Rollback(context.Background())
		cancel()
		if !joined {
			<-done
		}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		e = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockTransferRoot%')`).Scan(&waiting)
		if e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("root wait not observed")
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE library_root SET health_revision=health_revision+1 WHERE id=$1`, in.RootID); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	before := confirmationState(t, f, in)
	e = <-done
	joined = true
	if !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("lock waited stale snapshot accepted", e)
	}
	if confirmationState(t, f, in) != before {
		t.Fatal("stale lock waited response wrote outputs")
	}
}
