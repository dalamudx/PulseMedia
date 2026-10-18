package app_test

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	mediaUsecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"os"
	"testing"
)

type deniedRecognitionRoot struct {
	ports.TransferRoot
	denied string
}

func (r deniedRecognitionRoot) Inspect(ctx context.Context, path string) (ports.FileIdentity, error) {
	if path == r.denied {
		return ports.FileIdentity{}, os.ErrPermission
	}
	return r.TransferRoot.Inspect(ctx, path)
}

type deniedRecognitionFS struct {
	ports.RootFilesystem
	denied string
}

func (f deniedRecognitionFS) OpenRoot(ctx context.Context, path string) (ports.TransferRoot, error) {
	r, e := f.RootFilesystem.OpenRoot(ctx, path)
	if e != nil {
		return nil, e
	}
	return deniedRecognitionRoot{r, f.denied}, nil
}
func (f deniedRecognitionFS) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	roots, release, e := f.RootFilesystem.LockRoots(ctx, paths...)
	if e != nil {
		return nil, nil, e
	}
	for path, r := range roots {
		roots[path] = deniedRecognitionRoot{r, f.denied}
	}
	return roots, release, nil
}
func TestMovieRecognizerPermissionErrorPreservesSource(t *testing.T) {
	for _, target := range []string{"file", "marker"} {
		t.Run(target, func(t *testing.T) {
			f, s, _ := physicalRecognitionFixture(t)
			ctx := context.Background()
			work := recognitionWork(t, f, s)
			denied := f.path
			if target == "marker" {
				denied = ".pulsemedia_root"
			}
			calls := 0
			repo := postgres.NewCheckedMovieRecognitionStore(f.pool, f.gen, work)
			if e := mediaUsecase.NewMovieRecognizer(deniedRecognitionFS{rootfs.NewFilesystem(), denied}, repo, movieLinkProvider{before: func() { calls++ }}).Recognize(ctx, f.root, f.path); e != nil {
				t.Fatal(e)
			}
			var outcome, reason string
			if e := f.pool.QueryRow(ctx, "SELECT outcome,reason FROM movie_recognition_work WHERE job_id=$1", work.Lease.ID).Scan(&outcome, &reason); e != nil {
				t.Fatal(e)
			}
			var facts, links int
			var retired bool
			if e := f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM unit_fact WHERE source_file_id=$1),(SELECT count(*) FROM library_file_unit WHERE file_id=$1),(SELECT retired_at IS NOT NULL FROM library_file WHERE id=$1)", f.file).Scan(&facts, &links, &retired); e != nil {
				t.Fatal(e)
			}
			if outcome != "review" || reason != "source_unavailable" || calls != 0 || facts != 0 || links != 0 || retired {
				t.Fatal(outcome, reason, calls, facts, links, retired)
			}
		})
	}
}
