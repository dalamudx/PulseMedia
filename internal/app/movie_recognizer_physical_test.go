package app_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/library"
	mediaUsecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func physicalRecognitionFixture(t *testing.T) (movieLinkFixture, *postgres.MovieRecognitionStore, string) {
	t.Helper()
	f := newMovieLinkFixture(t)
	ctx := context.Background()
	root, err := f.q.GetLibraryRootByID(ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root.Path, ".pulsemedia_root"), []byte("synthetic root marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root.Path, f.path), []byte("native frozen file contents"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := postgres.NewScanObservationStore(f.pool)
	if _, err = library.NewScanner(f.gen, rootfs.NewFilesystem(), repo).ScanRoot(ctx, f.root); err != nil {
		t.Fatal(err)
	}
	file, err := f.q.GetActiveFileByPath(ctx, db.GetActiveFileByPathParams{RootID: f.root, RelPath: f.path})
	if err != nil {
		t.Fatal(err)
	}
	f.file = file.ID
	return f, postgres.NewMovieRecognitionStore(f.pool, f.gen), root.Path
}
func TestMovieRecognizerPhysicalSourceChecks(t *testing.T) {
	for _, phase := range []string{"before_metadata", "during_metadata"} {
		for _, change := range []string{"none", "missing", "inode", "symlink", "marker", "root"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				f, s, path := physicalRecognitionFixture(t)
				ctx := context.Background()
				work := recognitionWork(t, f, s)
				repo := postgres.NewCheckedMovieRecognitionStore(f.pool, f.gen, work)
				filePath := filepath.Join(path, f.path)
				mutate := func() {
					switch change {
					case "none":
					case "missing":
						if err := os.Remove(filePath); err != nil {
							t.Fatal(err)
						}
					case "inode":
						info, err := os.Stat(filePath)
						if err != nil {
							t.Fatal(err)
						}
						data, err := os.ReadFile(filePath)
						if err != nil {
							t.Fatal(err)
						}
						if err = os.Rename(filePath, filePath+".previous"); err != nil {
							t.Fatal(err)
						}
						if err = os.WriteFile(filePath, data, 0600); err != nil {
							t.Fatal(err)
						}
						if err = os.Chtimes(filePath, info.ModTime(), info.ModTime()); err != nil {
							t.Fatal(err)
						}
					case "symlink":
						if err := os.Rename(filePath, filePath+".previous"); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(f.path+".previous", filePath); err != nil {
							t.Fatal(err)
						}
					case "marker":
						if err := os.Remove(filepath.Join(path, ".pulsemedia_root")); err != nil {
							t.Fatal(err)
						}
					case "root":
						if err := os.Rename(path, path+".previous"); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if err := os.RemoveAll(path + ".previous"); err != nil {
								t.Error(err)
							}
						})
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(path, ".pulsemedia_root"), []byte("replacement"), 0600); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filePath, []byte("native frozen file contents"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				calls := 0
				if phase == "before_metadata" {
					mutate()
				}
				provider := movieLinkProvider{before: func() {
					calls++
					if phase == "during_metadata" {
						mutate()
					}
				}}
				if err := mediaUsecase.NewMovieRecognizer(rootfs.NewFilesystem(), repo, provider).Recognize(ctx, f.root, f.path); err != nil {
					t.Fatal(err)
				}
				var outcome, reason string
				var facts, mappings, audits, events int
				if err := f.pool.QueryRow(ctx, `SELECT outcome,COALESCE(reason,'') FROM movie_recognition_work WHERE job_id=$1`, work.Lease.ID).Scan(&outcome, &reason); err != nil {
					t.Fatal(err)
				}
				if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM unit_fact WHERE source_file_id=$1),(SELECT count(*) FROM library_file_unit WHERE file_id=$1),(SELECT count(*) FROM audit_log WHERE target_id=$1::text),(SELECT count(*) FROM outbox_event WHERE aggregate_id=$2)`, f.file, work.Lease.ID.String()).Scan(&facts, &mappings, &audits, &events); err != nil {
					t.Fatal(err)
				}
				expected := "review"
				expectedCalls := 0
				if phase == "during_metadata" || change == "none" {
					expectedCalls = 1
				}
				if change == "none" {
					expected = "linked"
					if facts != 1 || mappings != 1 {
						t.Fatal(facts, mappings)
					}
				} else if reason != "source_unavailable" || facts != 0 || mappings != 0 {
					t.Fatal(reason, facts, mappings)
				}
				if outcome != expected || calls != expectedCalls || audits != 1 || events != 1 {
					t.Fatal(outcome, calls, audits, events)
				}
				var retired bool
				if err := f.pool.QueryRow(ctx, `SELECT retired_at IS NOT NULL FROM library_file WHERE id=$1`, f.file).Scan(&retired); err != nil || retired {
					t.Fatal(retired, err)
				}
			})
		}
	}
}

func TestMovieRecognizerRequiresFreshSealedPhysicalProof(t *testing.T) {
	for _, mode := range []string{"absent", "expired", "future", "wrong_directory", "wrong_scan"} {
		t.Run(mode, func(t *testing.T) {
			f, s, _ := physicalRecognitionFixture(t)
			ctx := context.Background()
			work := recognitionWork(t, f, s)
			repo := postgres.NewCheckedMovieRecognitionStore(f.pool, f.gen, work)
			source, err := repo.LoadSource(ctx, f.root, f.path)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := repo.BeginFileCheck(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			check := &proof
			switch mode {
			case "absent":
				check = nil
			case "expired":
				proof.StartedAt = proof.StartedAt.Add(-6 * time.Second)
			case "future":
				proof.StartedAt = proof.StartedAt.Add(time.Minute)
			case "wrong_directory":
				proof.RootIdentity.Inode++
			case "wrong_scan":
				proof.ScanID = f.gen.NewUUID()
			}
			meta, _ := movieLinkProvider{}.GetMovieDetails(ctx, 550, "")
			if _, err = repo.Link(ctx, ports.MovieLinkInput{Source: source, Metadata: *meta, QualityJSON: []byte(`{}`), PhysicalCheck: check}); !errors.Is(err, ports.ErrMoviePhysicalCheck) {
				t.Fatal(err)
			}
			var count int
			if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM media`).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
			if mode == "absent" {
				if _, err = repo.Link(ctx, ports.MovieLinkInput{Source: source, Metadata: *meta, QualityJSON: []byte(`{}`), PhysicalCheck: &proof}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
