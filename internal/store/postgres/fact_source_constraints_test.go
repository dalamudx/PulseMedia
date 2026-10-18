package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type factSourceFixture struct {
	pool                      *pgxpool.Pool
	q                         *db.Queries
	dsn                       string
	file, root, first, second uuid.UUID
}

func newFactSourceFixture(t *testing.T) factSourceFixture {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, migrationsDir); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := postgres.Queries(pool)
	media, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "Synthetic source model", MediaType: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: media.ID, UnitKey: "S01E01"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: media.ID, UnitKey: "S01E02"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: "/synthetic/source-model", Name: "Synthetic", MediaType: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: root.ID, RelPath: "S01E01.mkv", SizeBytes: 42, MtimeNs: time.Now().UnixNano()})
	if err != nil {
		t.Fatal(err)
	}
	return factSourceFixture{pool: pool, q: q, dsn: dsn, file: file.ID, root: root.ID, first: first.ID, second: second.ID}
}
func requireForeignKeyFailure(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("expected source FK rejection, got %v", err)
	}
}
func TestFactSourceRejectsDanglingTaskAndUnmappedUnit(t *testing.T) {
	for _, kind := range []string{"dangling_task", "unmapped_file_unit"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactSourceFixture(t)
			ctx := context.Background()
			if err := f.q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: f.file, UnitID: f.first}); err != nil {
				t.Fatal(err)
			}
			fact := db.CreateUnitFactParams{ID: uuid.New(), UnitID: f.second, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","raw":"synthetic"}`)}
			if kind == "dangling_task" {
				fact.Stage = "submitted"
				fact.SourceTaskID = pgtype.UUID{Bytes: uuid.New(), Valid: true}
			} else {
				fact.Stage = "imported"
				fact.SourceFileID = pgtype.UUID{Bytes: f.file, Valid: true}
			}
			_, err := f.q.CreateUnitFact(ctx, fact)
			requireForeignKeyFailure(t, err)
			var remaining int
			if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE id=$1", fact.ID).Scan(&remaining); err != nil || remaining != 0 {
				t.Fatalf("rejected source left fact: %d %v", remaining, err)
			}
			good, err := f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: f.first, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: f.file, Valid: true}, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: []byte(`{}`)})
			if err != nil || good.UnitID != f.first {
				t.Fatalf("valid source rejected: %v", err)
			}
		})
	}
}
func TestFactSourceDeferredMappingCommitsAtomically(t *testing.T) {
	f := newFactSourceFixture(t)
	ctx := context.Background()
	for _, finishMapping := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing_mapping_rollback", true: "fact_then_mapping_commit"}[finishMapping], func(t *testing.T) {
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			id := uuid.New()
			q := f.q.WithTx(tx)
			if _, err := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: id, UnitID: f.second, Stage: "confirmed", SourceFileID: pgtype.UUID{Bytes: f.file, Valid: true}, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: []byte(`{}`)}); err != nil {
				t.Fatalf("source check must defer until commit: %v", err)
			}
			if finishMapping {
				if err := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: f.file, UnitID: f.second}); err != nil {
					t.Fatal(err)
				}
			}
			err = tx.Commit(ctx)
			if !finishMapping {
				requireForeignKeyFailure(t, err)
			} else if err != nil {
				t.Fatal(err)
			}
			var facts int
			if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE id=$1", id).Scan(&facts); err != nil {
				t.Fatal(err)
			}
			if (!finishMapping && facts != 0) || (finishMapping && facts != 1) {
				t.Fatalf("fact not atomic with mapping: %d", facts)
			}
		})
	}
}

func TestFactSourceMigrationPreservesLegacyAndRollback(t *testing.T) {
	f := newFactSourceFixture(t)
	ctx := context.Background()
	rollbackTo46 := func() {
		t.Helper()
		for version := int64(postgres.CurrentMigrationVersion); version > 46; version-- {
			if next, err := postgres.Rollback(ctx, f.dsn, migrationsDir); err != nil || next != version-1 {
				t.Fatalf("rollback: version=%d err=%v", next, err)
			}
		}
	}
	rollbackTo46()
	id := uuid.New()
	if _, err := f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: id, UnitID: f.second, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: f.file, Valid: true}, SourceTaskID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p","legacy":"unchanged"}`)}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		if err := f.pool.QueryRow(ctx, "SELECT to_jsonb(f)::text FROM unit_fact f WHERE id=$1", id).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	for round := 0; round < 2; round++ {
		if version, err := postgres.Migrate(ctx, f.dsn, migrationsDir); err != nil || version != postgres.CurrentMigrationVersion {
			t.Fatalf("upgrade: version=%d err=%v", version, err)
		}
		if snapshot() != before {
			t.Fatal("upgrade rewrote legacy evidence")
		}
		var notValidated int
		if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM pg_constraint WHERE conrelid='unit_fact'::regclass AND conname IN ('unit_fact_source_task_fk','unit_fact_source_file_unit_fk') AND NOT convalidated AND condeferrable AND condeferred").Scan(&notValidated); err != nil || notValidated != 2 {
			t.Fatalf("legacy-safe constraints: %d %v", notValidated, err)
		}
		_, err := f.q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: f.second, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: f.file, Valid: true}, RootID: pgtype.UUID{Bytes: f.root, Valid: true}, QualityProfile: []byte(`{}`)})
		requireForeignKeyFailure(t, err)
		if round == 0 {
			rollbackTo46()
			if snapshot() != before {
				t.Fatal("rollback rewrote legacy evidence")
			}
		}
	}
	// Historical invalid evidence may be revoked without rewriting source keys.
	if _, err := f.pool.Exec(ctx, "UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE id=$1", id); err != nil {
		t.Fatalf("legacy revocation blocked: %v", err)
	}
}

func TestModelUniquenessEightWay(t *testing.T) {
	for _, kind := range []string{"identity", "active_file_path"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactSourceFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var media uuid.UUID
			if err := f.pool.QueryRow(ctx, "SELECT media_id FROM media_unit WHERE id=$1", f.first).Scan(&media); err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 8)
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if kind == "identity" {
						_, err := f.q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: media, Namespace: "tmdb.tv", ExternalID: "synthetic-shared"})
						results <- err
					} else {
						_, err := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: f.root, RelPath: "race.mkv", SizeBytes: 42, MtimeNs: 1})
						results <- err
					}
				}()
			}
			close(start)
			wg.Wait()
			successes := 0
			for i := 0; i < 8; i++ {
				err := <-results
				if err == nil {
					successes++
					continue
				}
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
					t.Fatalf("unexpected concurrent result: %v", err)
				}
			}
			if successes != 1 {
				t.Fatalf("expected one winner, got %d", successes)
			}
			if kind == "active_file_path" {
				old, err := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: "race.mkv"})
				if err != nil {
					t.Fatal(err)
				}
				if err := f.q.RetireFileByPath(ctx, db.RetireFileByPathParams{RootID: f.root, RelPath: "race.mkv"}); err != nil {
					t.Fatal(err)
				}
				replacement, err := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: f.root, RelPath: "race.mkv", SizeBytes: 100, MtimeNs: 2})
				if err != nil || replacement.ID == old.ID {
					t.Fatalf("new incarnation: %v", err)
				}
				history, err := f.q.GetFileByID(ctx, old.ID)
				if err != nil || !history.RetiredAt.Valid || history.SizeBytes != 42 || history.MtimeNs != 1 {
					t.Fatalf("old incarnation changed: %+v %v", history, err)
				}
			}
		})
	}
}
