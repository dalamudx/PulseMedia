package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	usecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"sync"
	"testing"
	"time"
)

func TestFileMappingHistoryAtomicRecoveryAndEvidence(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, e := postgres.Migrate(ctx, dsn, migrationsDir); e != nil {
		t.Fatal(e)
	}
	pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	q := postgres.Queries(pool)
	gen := idgen.New()
	svc := usecase.NewFileMappingService(postgres.NewFileMappingStore(pool, gen))
	admin, e := q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "mapping-admin", PasswordHash: "fixture-not-used-for-login", Role: "admin", IsActive: true})
	if e != nil {
		t.Fatal(e)
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID.String(), Role: ports.RoleAdmin}
	root, e := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: t.TempDir(), MediaType: "tv", Name: "Mapping fixture"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "UPDATE library_root SET baseline_completed=true WHERE id=$1", root.ID); e != nil {
		t.Fatal(e)
	}
	m, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "Mapping Fixture Show", MediaType: "tv"})
	if e != nil {
		t.Fatal(e)
	}
	units := []uuid.UUID{}
	for ep := 1; ep <= 3; ep++ {
		id := uuid.New()
		if _, e = q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: id, MediaID: m.ID, UnitKey: fmt.Sprintf("S01E%02d", ep), SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: int32(ep), Valid: true}}); e != nil {
			t.Fatal(e)
		}
		units = append(units, id)
	}
	file, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: root.ID, RelPath: "Fixture.Show.S01E01E03.mkv", SizeBytes: 4096, MtimeNs: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: units[0]}); e != nil {
		t.Fatal(e)
	}
	expiry := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	imported, e := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: units[0], Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","source":"WEB-DL"}`), ExpiresAt: pgtype.Timestamptz{Time: expiry, Valid: true}})
	if e != nil {
		t.Fatal(e)
	}
	confirmed, e := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: units[0], Stage: "confirmed", SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, QualityProfile: []byte(`{}`)})
	if e != nil {
		t.Fatal(e)
	}
	p, e := svc.Preview(ctx, actor, file.ID, m.ID)
	if e != nil || !p.CanCorrect {
		t.Fatal("valid persisted imported fixture not editable", p, e)
	}
	t.Run("unit_before_media_matches_metadata_refresh", func(t *testing.T) {
		work, cancel := context.WithTimeout(ctx, 10*time.Second)
		tx, e := pool.Begin(work)
		if e != nil {
			t.Fatal(e)
		}
		locked := db.New(tx)
		if _, e = locked.LockScopeMediaUnits(work, m.ID); e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() {
			_, e := svc.Correct(work, actor, file.ID, ports.FileMappingCommand{MediaID: m.ID, UnitIDs: []uuid.UUID{units[0]}, ExpectedStamp: p.InputStamp, ConfirmedCoverage: true}, "mapping-lock-order")
			done <- e
		}()
		joined := false
		defer func() {
			_ = tx.Rollback(context.WithoutCancel(work))
			cancel()
			if !joined {
				<-done
			}
		}()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			var blocked bool
			if e = pool.QueryRow(work, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionUnit%')").Scan(&blocked); e != nil {
				t.Fatal(e)
			}
			if blocked {
				break
			}
			select {
			case <-ticker.C:
			case <-work.Done():
				t.Fatal("command never reached unit lock")
			}
		}
		if _, e = tx.Exec(work, "SELECT id FROM media WHERE id=$1 FOR NO KEY UPDATE NOWAIT", m.ID); e != nil {
			t.Fatal("mapping took media before waiting for unit; metadata refresh can deadlock", e)
		}
		if e = tx.Rollback(work); e != nil {
			t.Fatal(e)
		}
		e = <-done
		joined = true
		if e != nil {
			t.Fatal("mapping did not resume after metadata lock release", e)
		}
	})
	command := ports.FileMappingCommand{MediaID: m.ID, UnitIDs: []uuid.UUID{units[1], units[2]}, ExpectedStamp: p.InputStamp, ConfirmedCoverage: true}
	if _, e = pool.Exec(ctx, "CREATE FUNCTION fail_mapping_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='mapping-once' THEN RAISE EXCEPTION 'fixture-private';END IF;RETURN NEW;END $$;CREATE TRIGGER fail_mapping_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_mapping_receipt()"); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Correct(ctx, actor, file.ID, command, "mapping-once"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("missing final receipt rollback", e)
	}
	rollback, e := svc.Preview(ctx, actor, file.ID, m.ID)
	if e != nil || rollback.InputStamp != p.InputStamp {
		t.Fatal("partial mapping/facts/revision committed", rollback, e)
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER fail_mapping_receipt ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := svc.Correct(ctx, actor, file.ID, command, "mapping-once")
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal("unstable receipt", e)
		}
	}
	history, e := q.ListFileMappingHistory(ctx, file.ID)
	if e != nil || len(history) != 3 {
		t.Fatal("historical pairs lost", history, e)
	}
	mapped, e := q.ListUnitsForFile(ctx, file.ID)
	if e != nil || len(mapped) != 2 {
		t.Fatal("retired relation remains active", mapped, e)
	}
	var revoked int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE id=ANY($1::uuid[]) AND revoked_at IS NOT NULL AND revocation_reason='file_mapping_corrected'", []uuid.UUID{imported.ID, confirmed.ID}).Scan(&revoked); e != nil || revoked != 2 {
		t.Fatal("old imported/confirmed not invalidated", revoked, e)
	}
	facts, e := q.ListFileMappingImportedEvidence(ctx, pgtype.UUID{Bytes: file.ID, Valid: true})
	if e != nil || len(facts) != 2 {
		t.Fatal(facts, e)
	}
	for _, fact := range facts {
		if !fact.ExpiresAt.Valid || !fact.ExpiresAt.Time.Equal(expiry) {
			t.Fatal("mapping escalated source evidence TTL", fact.ExpiresAt)
		}
	}
	if _, e = svc.Correct(ctx, actor, file.ID, command, "stale-mapping"); !errors.Is(e, ports.ErrFileMappingConflict) {
		t.Fatal("old stamp accepted", e)
	}
	if _, e = svc.Correct(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: actor.ID, Role: ports.RoleMember}, file.ID, command, "mapping-once"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("cached permission elevation", e)
	}
	current, e := svc.Preview(ctx, actor, file.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	command.ExpectedStamp = current.InputStamp
	command.UnitIDs = []uuid.UUID{units[0]}
	if _, e = pool.Exec(ctx, "UPDATE media_unit SET metadata_version=metadata_version+1 WHERE id=$1", units[0]); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Correct(ctx, actor, file.ID, command, "metadata-changed"); !errors.Is(e, ports.ErrFileMappingConflict) {
		t.Fatal("old metadata stamp accepted", e)
	}
	current, e = svc.Preview(ctx, actor, file.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	command.ExpectedStamp = current.InputStamp
	if _, e = svc.Correct(ctx, actor, file.ID, command, "reactivate-history"); e != nil {
		t.Fatal(e)
	}
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE id=ANY($1::uuid[]) AND revoked_at IS NOT NULL", []uuid.UUID{imported.ID, confirmed.ID}).Scan(&revoked); e != nil || revoked != 2 {
		t.Fatal("reactivation revived historical imported/confirmed", revoked, e)
	}
	current, e = svc.Preview(ctx, actor, file.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	command.ExpectedStamp = current.InputStamp
	command.UnitIDs = []uuid.UUID{units[1]}
	if _, e = pool.Exec(ctx, "UPDATE unit_fact SET expires_at=clock_timestamp()-interval '1 second' WHERE source_file_id=$1 AND stage='imported' AND revoked_at IS NULL", file.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Correct(ctx, actor, file.ID, command, "expired-source"); !errors.Is(e, ports.ErrFileMappingConflict) {
		t.Fatal("expired source escalated", e)
	}
	if _, e = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: units[0], Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","source":"WEB-DL"}`)}); e != nil {
		t.Fatal(e)
	}
	current, e = svc.Preview(ctx, actor, file.ID, m.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "UPDATE library_root SET health='offline' WHERE id=$1", root.ID); e != nil {
		t.Fatal(e)
	}
	command.ExpectedStamp = current.InputStamp
	command.UnitIDs = []uuid.UUID{units[0]}
	if _, e = svc.Correct(ctx, actor, file.ID, command, "offline"); !errors.Is(e, ports.ErrFileMappingConflict) {
		t.Fatal("offline source accepted", e)
	}
	unknown, e := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: root.ID, RelPath: "Unknown.mkv", SizeBytes: 4096, MtimeNs: 1})
	if e != nil {
		t.Fatal(e)
	}
	p, e = svc.Preview(ctx, actor, unknown.ID, m.ID)
	if e != nil || p.CanCorrect {
		t.Fatal("mere index manufactured imported evidence", p, e)
	}
	for expected := int64(postgres.CurrentMigrationVersion) - 1; expected >= 29; expected-- {
		if v, e := postgres.Rollback(ctx, dsn, migrationsDir); e != nil || v != expected {
			t.Fatal("empty notification schema down", v, e)
		}
	}
	if _, e = postgres.Rollback(ctx, dsn, migrationsDir); e == nil {
		t.Fatal("downgrade revived historical mappings")
	}
	if e = postgres.CheckSchemaVersion(ctx, pool, 29); e != nil {
		t.Fatal("failed downgrade changed schema", e)
	}
}
