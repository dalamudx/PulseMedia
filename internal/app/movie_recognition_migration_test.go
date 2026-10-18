package app_test

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	mediaUsecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"github.com/google/uuid"
	"testing"
)

func TestMovieRecognitionMigrationPreservesUnscopedFileHistory(t *testing.T) {
	f := newMovieLinkFixture(t)
	ctx := context.Background()
	if _, e := mediaUsecase.NewMatcher(postgres.NewMovieLinkStore(f.pool, f.gen), movieLinkProvider{}).MatchAndLinkFile(ctx, f.root, f.path); e != nil {
		t.Fatal(e)
	}
	before := movieLinkState(t, f)
	v := int64(postgres.CurrentMigrationVersion)
	for v > 51 {
		var e error
		v, e = postgres.Rollback(ctx, f.pool.Config().ConnConfig.ConnString(), migrationsDir)
		if e != nil {
			t.Fatal(e)
		}
	}
	if v != 51 || movieLinkState(t, f) != before {
		t.Fatal("empty downgrade modified prior file history", v)
	}
	if v, e := postgres.Migrate(ctx, f.pool.Config().ConnConfig.ConnString(), migrationsDir); e != nil || v != postgres.CurrentMigrationVersion {
		t.Fatal(v, e)
	}
	if movieLinkState(t, f) != before {
		t.Fatal("upgrade modified prior file history")
	}
	var n int
	if e := f.pool.QueryRow(ctx, "SELECT count(*) FROM movie_recognition_work").Scan(&n); e != nil || n != 0 {
		t.Fatal("historical source certified", n, e)
	}
}
func TestMovieRecognitionQueueFixedBatchWithoutDuplicateCandidates(t *testing.T) {
	f, s := recognitionFixture(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		file, e := f.q.CreateLibraryFile(ctx, db.CreateLibraryFileParams{ID: uuid.New(), RootID: f.root, RelPath: fmt.Sprintf("candidate-%d.mkv", i), SizeBytes: 100, MtimeNs: 1})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.pool.Exec(ctx, "UPDATE library_file SET file_fingerprint='native-fixture' WHERE id=$1", file.ID); e != nil {
			t.Fatal(e)
		}
	}
	for _, want := range []int{25, 1, 0} {
		if n, e := s.QueueDue(ctx, 20); e != nil || n != want {
			t.Fatal("unbounded/repeated scheduling", want, n, e)
		}
	}
}
