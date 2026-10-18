package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquisitionWaitsForImportAndRechecksOwnership(t *testing.T) {
	f := organizationSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	task, err := f.q.GetDownloadTaskByOpID(ctx, f.op)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := f.q.WithTx(tx)
	if _, err := q.LockTransferRoot(ctx, f.root); err != nil {
		t.Fatal(err)
	}
	if _, err := q.LockTransferUnit(ctx, f.unit); err != nil {
		t.Fatal(err)
	}
	svc := acquisition.NewService(postgres.NewAcquisitionStore(f.pool, f.gen, clock.New(), acquisition.SelectMovieRound))
	actor := pgtest.CreateUserActor(t, f.q, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin})
	result := make(chan error, 1)
	go func() {
		_, err := svc.AcquireMovie(ctx, actor, acquisition.AcquireMovieCommand{RootID: f.root, UnitIDs: []uuid.UUID{f.unit}, CandidateID: task.CandidateID})
		result <- err
		close(result)
	}()
	defer func() { cancel(); _ = tx.Rollback(context.WithoutCancel(ctx)); <-result }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := f.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockAcquisitionRoot%')").Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("acquisition did not serialize on import root")
		case <-ticker.C:
		}
	}
	// Install actual hardlinked bytes and imported evidence under the other PG transaction.
	target := filepath.Join(f.target, "already.mkv")
	if err := os.Link(f.path, target); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	file, err := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: f.root, RelPath: "already.mkv", SizeBytes: st.Size(), MtimeNs: st.ModTime().UnixNano(), LastMutationSeq: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: f.unit}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: f.unit, Stage: "imported", RootID: pgtype.UUID{Bytes: f.root, Valid: true}, SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, QualityProfile: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	if err := q.ConsumeTransferReservation(ctx, db.ConsumeTransferReservationParams{OperationID: f.op, UnitID: f.unit, RootID: f.root}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.IncrementRootMutationSeq(ctx, f.root); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, acquisition.ErrOwnershipUncertain) {
		t.Fatalf("late acquisition failed to hold the committed unknown-quality import: %v", err)
	}
	if f.count(t, "job") != 0 {
		t.Fatal("late acquisition submitted duplicate download")
	}
	reqs, err := f.q.ListRequirementsForOperation(ctx, f.op)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("original demand changed: %+v %v", reqs, err)
	}
}
