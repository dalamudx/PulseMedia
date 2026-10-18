package library_test

import (
	"context"
	"errors"
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

type missingInspectRoot struct {
	ports.TransferRoot
	hook func(context.Context, string) (ports.FileIdentity, error)
}

func (r missingInspectRoot) Inspect(ctx context.Context, rel string) (ports.FileIdentity, error) {
	return r.hook(ctx, rel)
}

type missingInspectFS struct {
	ports.ScanFilesystem
	hook func(ports.TransferRoot, context.Context, string) (ports.FileIdentity, error)
}

func (f missingInspectFS) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	roots, release, err := f.ScanFilesystem.LockRoots(ctx, paths...)
	if err != nil {
		return nil, nil, err
	}
	for p, r := range roots {
		roots[p] = missingInspectRoot{TransferRoot: r, hook: func(ctx context.Context, rel string) (ports.FileIdentity, error) { return f.hook(r, ctx, rel) }}
	}
	return roots, release, nil
}
func TestMissingConfirmerNativeFilesystem(t *testing.T) {
	for _, kind := range []string{"absent", "present", "permission", "symlink", "marker_lost", "root_replaced"} {
		t.Run(kind, func(t *testing.T) {
			scanner, q, dsn := setupScannerTest(t)
			ctx := context.Background()
			pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			path := t.TempDir()
			rel := "Movie.mkv"
			target := filepath.Join(path, rel)
			marker := filepath.Join(path, ".pulsemedia_root")
			if err = os.WriteFile(marker, []byte("marker"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(target, []byte("media"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Path: path, MediaType: "movie", Name: "Movies"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = scanner.ScanRoot(ctx, root.ID); err != nil {
				t.Fatal(err)
			}
			file, err := q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: root.ID, RelPath: rel})
			if err != nil {
				t.Fatal(err)
			}
			media, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), MediaType: "movie", Title: "Fixture"})
			if err != nil {
				t.Fatal(err)
			}
			unit, err := q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: uuid.New(), MediaID: media.ID, UnitKey: "movie"})
			if err != nil {
				t.Fatal(err)
			}
			if err = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: unit.ID}); err != nil {
				t.Fatal(err)
			}
			for _, stage := range []string{"imported", "confirmed"} {
				if _, err = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: uuid.New(), UnitID: unit.ID, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, Stage: stage, SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, QualityProfile: []byte(`{"sentinel":"original evidence"}`)}); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.Remove(target); err != nil {
				t.Fatal(err)
			}
			first, err := scanner.ScanRoot(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			second, err := scanner.ScanRoot(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, "UPDATE scan_run SET finished_at=finished_at-interval '10 minutes' WHERE id=$1", first.ScanRunID); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var s string
				if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('file',(SELECT to_jsonb(f) FROM library_file f WHERE id=$1),'mapping',(SELECT jsonb_agg(to_jsonb(m)) FROM library_file_unit m WHERE file_id=$1),'facts',(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM unit_fact f WHERE source_file_id=$1),'reservations',(SELECT jsonb_agg(to_jsonb(r)) FROM unit_reservation r))::text`, file.ID).Scan(&s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			before := snapshot()
			var fs ports.ScanFilesystem = rootfs.NewFilesystem()
			switch kind {
			case "present":
				if err = os.WriteFile(target, []byte("reappeared"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err = os.Symlink(marker, target); err != nil {
					t.Fatal(err)
				}
			case "permission", "marker_lost", "root_replaced":
				fs = missingInspectFS{ScanFilesystem: fs, hook: func(r ports.TransferRoot, ctx context.Context, p string) (ports.FileIdentity, error) {
					if p != rel {
						return r.Inspect(ctx, p)
					}
					if kind == "permission" {
						return ports.FileIdentity{}, os.ErrPermission
					}
					id, e := r.Inspect(ctx, p)
					if kind == "marker_lost" {
						if err := os.Remove(marker); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.Rename(path, path+".old"); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if err := os.RemoveAll(path + ".old"); err != nil {
								t.Error(err)
							}
						})
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(marker, []byte("new root"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return id, e
				}}
			}
			confirmer := library.NewMissingConfirmer(fs, postgres.NewScanObservationStore(pool))
			n, err := confirmer.Confirm(ctx, root.ID, second.ScanRunID)
			if kind == "absent" {
				if err != nil || n != 1 {
					t.Fatal(n, err)
				}
				var retired, revoked bool
				if e := pool.QueryRow(ctx, "SELECT retired_at IS NOT NULL FROM library_file WHERE id=$1", file.ID).Scan(&retired); e != nil || !retired {
					t.Fatal(retired, e)
				}
				if e := pool.QueryRow(ctx, "SELECT bool_and(revoked_at IS NOT NULL AND quality_profile->>'sentinel'='original evidence') FROM unit_fact WHERE source_file_id=$1", file.ID).Scan(&revoked); e != nil || !revoked {
					t.Fatal(revoked, e)
				}
			} else {
				if n != 0 {
					t.Fatal("unexpected missing count", n)
				}
				if kind == "present" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil {
					t.Fatal("inconclusive filesystem result accepted")
				}
				if kind == "permission" && !errors.Is(err, os.ErrPermission) {
					t.Fatal(err)
				}
				if after := snapshot(); after != before {
					t.Fatal("inconclusive/present check modified protected facts")
				}
			}
			var receipts int
			if e := pool.QueryRow(ctx, "SELECT count(*) FROM scan_missing_receipt").Scan(&receipts); e != nil {
				t.Fatal(e)
			}
			expected := 0
			if kind == "absent" || kind == "present" {
				expected = 1
			}
			if receipts != expected {
				t.Fatal(receipts, expected)
			}
		})
	}
}
