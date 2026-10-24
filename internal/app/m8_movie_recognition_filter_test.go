package app_test

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	mediaUsecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
)

func TestM8MovieRecognitionFilterKeepsScanInventoryPG(t *testing.T) {
	for _, mode := range []string{"sample", "trailer_directory", "hidden", "below_minimum", "empty"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := recognitionFixture(t)
			path, size := f.path, int64(100)
			switch mode {
			case "sample":
				path = "Fight.Club.1999.sample.mkv"
			case "trailer_directory":
				path = "Trailers/" + path
			case "hidden":
				path = ".hidden/" + path
			case "empty":
				size = 0
			}
			minimum := int64(100)
			if mode == "below_minimum" {
				minimum++
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE library_file SET rel_path=$1,size_bytes=$2 WHERE id=$3`, path, size, f.file); err != nil {
				t.Fatal(err)
			}
			before := recognitionState(t, f)
			scheduler := postgres.NewMovieRecognitionStoreWithMinimumMediaBytes(f.pool, f.gen, minimum)
			if n, err := scheduler.QueueDue(t.Context(), 20); err != nil || n != 0 {
				t.Fatal("excluded scan file entered metadata queue", n, err)
			}
			calls := 0
			repo := postgres.NewMovieLinkStoreWithMinimumMediaBytes(f.pool, f.gen, minimum)
			if _, err := repo.LoadSource(t.Context(), f.root, path); !errors.Is(err, ports.ErrMovieLinkStale) {
				t.Fatal("excluded file bypassed repository source filter", err)
			}
			matcher := mediaUsecase.NewMatcher(repo, movieLinkProvider{before: func() { calls++ }})
			_, err := matcher.MatchAndLinkFile(t.Context(), f.root, path)
			want := ports.ErrMovieLinkStale
			if mode == "sample" {
				want = mediaUsecase.ErrLowConfidence
			}
			if !errors.Is(err, want) {
				t.Fatal("excluded file bypassed recognition", err)
			}
			if calls != 0 || before != recognitionState(t, f) {
				t.Fatal("excluded file changed inventory, missing state, tasks or media facts")
			}
		})
	}
}

func TestM8MovieRecognitionFilterRechecksQueuedAndCapturedInputPG(t *testing.T) {
	f, scheduler := recognitionFixture(t)
	work := recognitionWork(t, f, scheduler)
	before := recognitionState(t, f)
	checked := postgres.NewCheckedMovieRecognitionStoreWithMinimumMediaBytes(f.pool, f.gen, work, 101)
	if _, err := checked.LoadSource(t.Context(), f.root, f.path); !errors.Is(err, ports.ErrMovieLinkStale) {
		t.Fatal("queued work bypassed higher deployment minimum", err)
	}
	source, err := postgres.NewMovieLinkStore(f.pool, f.gen).LoadSource(t.Context(), f.root, f.path)
	if err != nil {
		t.Fatal(err)
	}
	higher := postgres.NewMovieLinkStoreWithMinimumMediaBytes(f.pool, f.gen, 101)
	if _, err = higher.Link(t.Context(), ports.MovieLinkInput{Source: source, Metadata: ports.MovieMetadata{TMDBID: 550, Title: "Fight Club", Year: 1999, MediaType: "movie"}, QualityJSON: []byte("{}")}); !errors.Is(err, ports.ErrMovieLinkStale) {
		t.Fatal("captured old source bypassed new rule at Link", err)
	}
	if before != recognitionState(t, f) {
		t.Fatal("filter changed queued lease, scan inventory or business facts")
	}
}

func TestM8MovieRecognitionFilterDoesNotStarveLaterPageOrRootPG(t *testing.T) {
	f, _ := recognitionFixture(t)
	highFile := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	if _, err := f.pool.Exec(t.Context(), `UPDATE library_file SET id=$1 WHERE id=$2`, highFile, f.file); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 25; i++ {
		id := uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012x", i))
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,$3,100,1,'sample-fixture')`, id, f.root, fmt.Sprintf("sample.%02d.mkv", i)); err != nil {
			t.Fatal(err)
		}
	}
	firstRoot := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO library_root(id,path,name,media_type,health,baseline_completed) VALUES($1,$2,'excluded root','movie','online',true)`, firstRoot, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns,file_fingerprint) VALUES($1,$2,'sample.mkv',100,1,'sample-fixture')`, uuid.New(), firstRoot); err != nil {
		t.Fatal(err)
	}
	beforeInventory := movieLinkState(t, f)
	s := postgres.NewMovieRecognitionStoreWithMinimumMediaBytes(f.pool, f.gen, 100)
	if n, err := s.QueueDue(t.Context(), 1); err != nil || n != 1 {
		t.Fatal("excluded root or first 25 files starved later primary", n, err)
	}
	var valid bool
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*)=1 AND bool_and(file_id=$1) FROM movie_recognition_work`, highFile).Scan(&valid); err != nil || !valid {
		t.Fatal("excluded file created a recognition task", err)
	}
	if n, err := s.QueueDue(t.Context(), 1); err != nil || n != 0 {
		t.Fatal("repeated queue created duplicate task", n, err)
	}
	if beforeInventory != movieLinkState(t, f) {
		t.Fatal("queue filtering changed scan or library facts")
	}
}

func TestM8MovieRecognitionAtMinimumAndWordBoundaryPG(t *testing.T) {
	f, _ := recognitionFixture(t)
	path := "Sampler.1999.{tmdb-550}.mkv"
	if _, err := f.pool.Exec(t.Context(), `UPDATE library_file SET rel_path=$1 WHERE id=$2`, path, f.file); err != nil {
		t.Fatal(err)
	}
	calls := 0
	matcher := mediaUsecase.NewMatcher(postgres.NewMovieLinkStoreWithMinimumMediaBytes(f.pool, f.gen, 100), movieLinkProvider{before: func() { calls++ }})
	if _, err := matcher.MatchAndLinkFile(t.Context(), f.root, path); err != nil {
		t.Fatal("valid word boundary or exact minimum rejected", err)
	}
	var imported int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM unit_fact WHERE source_file_id=$1 AND stage='imported'`, f.file).Scan(&imported); err != nil || imported != 1 || calls != 1 {
		t.Fatal("valid scan file did not import exactly once", imported, calls, err)
	}
}
