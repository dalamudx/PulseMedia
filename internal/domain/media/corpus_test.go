package media_test

import (
	"fmt"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

type SyntheticSample struct {
	Filename       string
	ExpectedType   media.Type
	ExpectedTitle  string
	ExpectedSeason int
	ExpectedEps    []int
	ExpectedReason string
	ExpectReview   bool
	ExpectedRes    quality.Resolution
}

func buildSyntheticCombinationSamples() []SyntheticSample {
	samples := make([]SyntheticSample, 0, 1000)

	movieTitles := []string{
		"Fight Club", "The Matrix", "Inception", "Interstellar", "Pulp Fiction",
		"The Dark Knight", "Forrest Gump", "Spirited Away", "Parasite", "Gladiator",
	}

	sources := []string{"Remux", "Bluray", "WEB-DL", "WEBRip", "HDTV"}
	resolutions := []string{"2160p", "1080p", "720p"}
	codecs := []string{"HEVC", "x264", "x265", "AV1"}
	groups := []string{"SPARKS", "FraMeSToR", "NTb", "FLUX", "DON"}

	// 1. 300 Movie Releases (10 titles x 5 sources x 3 res x 2 codecs)
	for _, title := range movieTitles {
		for _, src := range sources {
			for _, res := range resolutions {
				for _, cdc := range codecs[:2] {
					year := 2010
					grp := groups[len(samples)%len(groups)]
					filename := fmt.Sprintf("%s.%d.%s.%s.%s-%s.mkv", title, year, res, src, cdc, grp)
					samples = append(samples, SyntheticSample{
						Filename:       filename,
						ExpectedType:   media.TypeMovie,
						ExpectedTitle:  title,
						ExpectedSeason: 0,
						ExpectedEps:    nil,
						ExpectedReason: "clean_movie_with_year",
						ExpectReview:   false,
						ExpectedRes:    quality.Resolution(res),
					})
				}
			}
		}
	}

	// 2. 300 Standard TV Episodes (S01E01 ... S05E12)
	tvShows := []string{"Breaking Bad", "Game of Thrones", "Stranger Things", "The Wire", "Chernobyl"}
	for _, show := range tvShows {
		for season := 1; season <= 5; season++ {
			for ep := 1; ep <= 12; ep++ {
				filename := fmt.Sprintf("%s.S%02dE%02d.1080p.WEB-DL-GROUP.mkv", show, season, ep)
				samples = append(samples, SyntheticSample{
					Filename:       filename,
					ExpectedType:   media.TypeTV,
					ExpectedTitle:  show,
					ExpectedSeason: season,
					ExpectedEps:    []int{ep},
					ExpectedReason: "standard_season_episode",
					ExpectReview:   false,
					ExpectedRes:    quality.Res1080p,
				})
			}
		}
	}

	// 3. 200 Multi-Episode Releases (S01E01-E02, S02E05-08, etc.)
	multiShows := []string{"Planet Earth", "Blue Planet", "Band of Brothers", "Fargo", "True Detective"}
	for _, show := range multiShows {
		for s := 1; s <= 4; s++ {
			for startEp := 1; startEp <= 19; startEp += 2 {
				endEp := startEp + 1
				eps := []int{startEp, endEp}
				filename := fmt.Sprintf("%s.S%02dE%02d-E%02d.720p.HDTV.mkv", show, s, startEp, endEp)
				samples = append(samples, SyntheticSample{
					Filename:       filename,
					ExpectedType:   media.TypeTV,
					ExpectedTitle:  show,
					ExpectedSeason: s,
					ExpectedEps:    eps,
					ExpectedReason: "multi_episode_range",
					ExpectReview:   false,
					ExpectedRes:    quality.Res720p,
				})
			}
		}
	}

	// 4. 150 Anime Absolute Episode Releases
	animeList := []string{"Attack on Titan", "Frieren", "Demon Slayer", "Jujutsu Kaisen", "One Piece"}
	animeGroups := []string{"SubsPlease", "Erai-raws", "HorribleSubs"}
	for _, anime := range animeList {
		for _, grp := range animeGroups {
			for ep := 1; ep <= 10; ep++ {
				filename := fmt.Sprintf("[%s] %s - %02d [1080p].mkv", grp, anime, ep)
				samples = append(samples, SyntheticSample{
					Filename:       filename,
					ExpectedType:   media.TypeTV,
					ExpectedTitle:  anime,
					ExpectedSeason: 0,
					ExpectedEps:    nil,
					ExpectedReason: "absolute_mapping_required",
					ExpectReview:   true,
					ExpectedRes:    quality.Res1080p,
				})
			}
		}
	}

	// 5. 50 Edge Cases (Noisy titles, unidentified, review required)
	for i := 1; i <= 50; i++ {
		filename := fmt.Sprintf("random_unidentified_clip_%03d.avi", i)
		samples = append(samples, SyntheticSample{
			Filename:       filename,
			ExpectedType:   media.TypeMovie,
			ExpectedTitle:  fmt.Sprintf("random unidentified clip %03d", i),
			ExpectedSeason: 0,
			ExpectedEps:    nil,
			ExpectedReason: "low_confidence_unidentified",
			ExpectReview:   true,
		})
	}

	return samples
}

func TestSyntheticCombinationCorpusRegression(t *testing.T) {
	corpus := buildSyntheticCombinationSamples()
	if len(corpus) < 1000 {
		t.Fatalf("expected at least 1000 synthetic combination samples, got %d", len(corpus))
	}

	failures := 0
	for i, sample := range corpus {
		rec := media.RecognizeRelease(sample.Filename)

		failed := false
		if rec.MediaType != sample.ExpectedType {
			t.Errorf("[%d: %s] Expected type %s, got %s", i, sample.Filename, sample.ExpectedType, rec.MediaType)
			failed = true
		}
		if rec.Title != sample.ExpectedTitle {
			t.Errorf("[%d: %s] Expected title %q, got %q", i, sample.Filename, sample.ExpectedTitle, rec.Title)
			failed = true
		}
		if rec.Season != sample.ExpectedSeason {
			t.Errorf("[%d: %s] Expected season %d, got %d", i, sample.Filename, sample.ExpectedSeason, rec.Season)
			failed = true
		}
		if len(sample.ExpectedEps) > 0 {
			if len(rec.Episodes) != len(sample.ExpectedEps) {
				t.Errorf("[%d: %s] Expected %d eps, got %d eps", i, sample.Filename, len(sample.ExpectedEps), len(rec.Episodes))
				failed = true
			} else {
				for j, ep := range sample.ExpectedEps {
					if rec.Episodes[j] != ep {
						t.Errorf("[%d: %s] Ep mismatch at %d: expected %d, got %d", i, sample.Filename, j, ep, rec.Episodes[j])
						failed = true
					}
				}
			}
		}
		if rec.RequiresReview != sample.ExpectReview {
			t.Errorf("[%d: %s] Expected review=%v, got %v", i, sample.Filename, sample.ExpectReview, rec.RequiresReview)
			failed = true
		}

		if failed {
			failures++
		}
	}

	if failures > 0 {
		t.Fatalf("synthetic combination regression failed with %d / %d failures", failures, len(corpus))
	}
	t.Logf("100%% PASS: All %d synthetic combination regression samples verified successfully!", len(corpus))
}

func BenchmarkRecognizeRelease(b *testing.B) {
	corpus := buildSyntheticCombinationSamples()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sample := corpus[i%len(corpus)]
		_ = media.RecognizeRelease(sample.Filename)
	}
}
