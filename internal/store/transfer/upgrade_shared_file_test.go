package transfer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/transfer"
)

func TestUpgradeSharedFilePreservesEveryUnit(t *testing.T) {
	svc, q, rootID, unitID, sourceRoot, targetRoot := setupTransferTest(t)
	ctx := context.Background()
	otherMedia, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "Other movie", MediaType: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: otherMedia.ID, UnitKey: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	oldBytes := []byte("unchanged shared library content")
	oldPath := filepath.Join(targetRoot, "shared.mkv")
	if err = os.WriteFile(oldPath, oldBytes, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	old, err := q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: rootID, RelPath: "shared.mkv", SizeBytes: info.Size(), MtimeNs: info.ModTime().UnixNano(), LastMutationSeq: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range []uuid.UUID{unitID, other.ID} {
		if err = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: old.ID, UnitID: unit}); err != nil {
			t.Fatal(err)
		}
		for _, stage := range []string{"imported", "confirmed"} {
			_, err = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit, Stage: stage, SourceFileID: pgtype.UUID{Bytes: old.ID, Valid: true}, RootID: pgtype.UUID{Bytes: rootID, Valid: true}, QualityProfile: []byte(`{"resolution":"1080p"}`)})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	sourcePath := filepath.Join(sourceRoot, "Fight.Club.1999.2160p.Remux.mkv")
	if err = os.WriteFile(sourcePath, []byte("new single-unit content"), 0600); err != nil {
		t.Fatal(err)
	}
	err = svc.UpgradeMediaFile(ctx, uuid.New(), rootID, unitID, old.ID, sourcePath, "Fight Club", 1999, quality.Profile{Resolution: quality.Res2160p, Source: quality.SourceRemux})
	current, readErr := q.GetFileByID(ctx, old.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !errors.Is(err, transfer.ErrReviewRequired) || current.RetiredAt.Valid {
		t.Fatalf("single-unit upgrade retired a shared file: error=%v retired=%v", err, current.RetiredAt.Valid)
	}
	for _, unit := range []uuid.UUID{unitID, other.ID} {
		files, readErr := q.ListFilesForUnit(ctx, unit)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(files) != 1 || files[0].ID != old.ID {
			t.Fatalf("changed active file for %s: %+v", unit, files)
		}
		facts, readErr := q.ListActiveFactsForUnit(ctx, unit)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(facts) != 2 {
			t.Fatalf("lost original imported/confirmed facts for %s: %+v", unit, facts)
		}
		for _, fact := range facts {
			if !fact.SourceFileID.Valid || uuid.UUID(fact.SourceFileID.Bytes) != old.ID {
				t.Fatalf("unexpected fact: %+v", fact)
			}
		}
	}
	after, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(oldBytes) {
		t.Fatal("shared source content changed")
	}
}
