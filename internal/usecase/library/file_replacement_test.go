package library_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"os"
	"path/filepath"
	"testing"
)

func TestScannerFileReplacementPreservesHistoryAndRevokesFacts(t *testing.T) {
	for _, kind := range []string{"metadata", "inode"} {
		t.Run(kind, func(t *testing.T) {
			scanner, q, dsn := setupScannerTest(t)
			ctx := context.Background()
			path := t.TempDir()
			rel := "Movie.2024.mkv"
			target := filepath.Join(path, rel)
			if err := os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("fixture marker"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("old media"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Movies"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = scanner.ScanRoot(ctx, root.ID); err != nil {
				t.Fatal(err)
			}
			old, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: rel})
			if err != nil {
				t.Fatal(err)
			}
			evidence := []byte(`{"source":"unknown","sentinel":"historical evidence preserved"}`)
			facts := []uuid.UUID{}
			for i := 0; i < 2; i++ {
				media, e := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), MediaType: "movie", Title: fmt.Sprintf("Movie %d", i)})
				if e != nil {
					t.Fatal(e)
				}
				unit, e := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: media.ID, UnitKey: "movie"})
				if e != nil {
					t.Fatal(e)
				}
				if e = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: old.ID, UnitID: unit.ID}); e != nil {
					t.Fatal(e)
				}
				stage := "imported"
				if i == 1 {
					stage = "confirmed"
				}
				fact, e := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit.ID, Stage: stage, SourceFileID: pgtype.UUID{Bytes: old.ID, Valid: true}, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, QualityProfile: evidence})
				if e != nil {
					t.Fatal(e)
				}
				facts = append(facts, fact.ID)
			}
			if kind == "metadata" {
				err = os.WriteFile(target, []byte("replacement media with changed size"), 0600)
			} else {
				before, e := os.Stat(target)
				if e != nil {
					t.Fatal(e)
				}
				tmp := filepath.Join(path, ".replacement")
				err = os.WriteFile(tmp, []byte("new media"), 0600)
				if err == nil {
					err = os.Chtimes(tmp, before.ModTime(), before.ModTime())
				}
				if err == nil {
					err = os.Rename(tmp, target)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			report, err := scanner.ScanRoot(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			active, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: rel})
			if err != nil {
				t.Fatal(err)
			}
			if active.ID == old.ID || report.FilesUpdated != 1 {
				t.Fatalf("replaced file retained old incarnation: old=%s active=%s updates=%d", old.ID, active.ID, report.FilesUpdated)
			}
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			var retained, revoked, mapped, newFacts int
			if err = pool.QueryRow(ctx, "SELECT count(*) FROM library_file WHERE id=$1 AND retired_at IS NOT NULL AND size_bytes=$2 AND mtime_ns=$3", old.ID, old.SizeBytes, old.MtimeNs).Scan(&retained); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(ctx, "SELECT count(*) FROM unit_fact WHERE id=ANY($1::uuid[]) AND revoked_at IS NOT NULL AND quality_profile=$2::jsonb", facts, evidence).Scan(&revoked); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(ctx, "SELECT count(*) FROM library_file_unit WHERE file_id=$1 AND retired_at IS NOT NULL", old.ID).Scan(&mapped); err != nil {
				t.Fatal(err)
			}
			if err = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM unit_fact WHERE source_file_id=$1)+(SELECT count(*) FROM library_file_unit WHERE file_id=$1)", active.ID).Scan(&newFacts); err != nil {
				t.Fatal(err)
			}
			if retained != 1 || revoked != 2 || mapped != 2 || newFacts != 0 {
				t.Fatalf("history/invalidations: retained=%d revoked=%d retiredMappings=%d inherited=%d", retained, revoked, mapped, newFacts)
			}
			if _, err = scanner.ScanRoot(ctx, root.ID); err != nil {
				t.Fatal(err)
			}
			again, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: rel})
			if err != nil || again.ID != active.ID {
				t.Fatalf("unchanged scan changed incarnation: %v", err)
			}

		})
	}
}

// An interrupted walk may commit positive observations but cannot infer absence.
type partialReplacementFilesystem struct{ ports.ScanFilesystem }

func (f partialReplacementFilesystem) Walk(ctx context.Context, root ports.TransferRoot, visit func(string, ports.FileIdentity) error) error {
	return f.ScanFilesystem.Walk(ctx, root, func(rel string, info ports.FileIdentity) error {
		if err := visit(rel, info); err != nil {
			return err
		}
		if rel == "A.Movie.mkv" {
			return errors.New("synthetic unreadable remaining directory")
		}
		return nil
	})
}
func TestScannerPartialReplacementDoesNotInvalidateUnobservedFile(t *testing.T) {
	scanner, q, dsn := setupScannerTest(t)
	ctx := context.Background()
	path := t.TempDir()
	for name, data := range map[string]string{".pulsemedia_root": "fixture marker", "A.Movie.mkv": "old media", "Z.Unobserved.mkv": "unobserved media"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Movies"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scanner.ScanRoot(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	old, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: "A.Movie.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	unseen, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: "Z.Unobserved.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	media, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), MediaType: "movie", Title: "Unobserved Movie"})
	if err != nil {
		t.Fatal(err)
	}
	unit, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: media.ID, UnitKey: "movie"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: unseen.ID, UnitID: unit.ID}); err != nil {
		t.Fatal(err)
	}
	fact, err := q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit.ID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: unseen.ID, Valid: true}, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, QualityProfile: []byte(`{"sentinel":"unobserved history"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(path, "A.Movie.mkv"), []byte("changed observed media"), 0600); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	interrupted := library.NewScanner(idgen.New(), partialReplacementFilesystem{rootfs.NewFilesystem()}, postgres.NewScanObservationStore(pool))
	if _, err = interrupted.ScanRoot(ctx, root.ID); err == nil {
		t.Fatal("interrupted walk returned success")
	}
	var partialRun uuid.UUID
	var state string
	if err = pool.QueryRow(ctx, "SELECT id,state FROM scan_run WHERE root_id=$1 ORDER BY scan_token DESC LIMIT 1", root.ID).Scan(&partialRun, &state); err != nil || state != "partial" {
		t.Fatalf("interrupted run state=%s err=%v", state, err)
	}
	active, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: "A.Movie.mkv"})
	if err != nil || active.ID == old.ID {
		t.Fatalf("positive replacement lost: %v", err)
	}
	after, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: "Z.Unobserved.mkv"})
	if err != nil || after.ID != unseen.ID {
		t.Fatalf("unobserved file invalidated: %v", err)
	}
	var valid bool
	var candidates int
	if err = pool.QueryRow(ctx, "SELECT revoked_at IS NULL FROM unit_fact WHERE id=$1", fact.ID).Scan(&valid); err != nil || !valid {
		t.Fatalf("unobserved fact invalidated: %v", err)
	}
	if err = pool.QueryRow(ctx, "SELECT files_missing_candidates FROM scan_run WHERE id=$1", partialRun).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("partial scan manufactured missing files: %d %v", candidates, err)
	}
}
