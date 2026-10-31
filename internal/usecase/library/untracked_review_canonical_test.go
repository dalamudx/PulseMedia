package library

import (
	"github.com/google/uuid"
	"testing"
)

func TestUntrackedReviewCanonicalArbitraryFilename(t *testing.T) {
	for _, path := range []string{"episode.mkv", "[Anime] 番剧 - 103.mkv", "Wrong.2020.S09E99.mkv", "Season 99/random-video.mkv"} {
		t.Run(path, func(t *testing.T) {
			f := untrackedReviewFixture()
			f.tv()
			f.path(path)
			in := &f.execution.capture.snapshot.Request.Input
			in.TMDBID = 42
			in.CanonicalUnitID = uuid.NewString()
			in.MediaMetadataVersion = 7
			in.UnitMetadataVersion = 9
			in.Season = 1
			in.Episode = 3
			in.TMDBEpisodeID = 403
			if err := f.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			e := f.execution.evidence
			if !e.Complete || e.Candidate == nil || len(e.Candidate.Units) != 1 || e.Candidate.Units[0].TMDBEpisodeID != 403 || e.Candidate.Units[0].UnitKey != "S01E03" {
				t.Fatalf("wrong explicit coverage: %+v", e)
			}
			untrackedReviewAssertClosed(t, f.files)
		})
	}
}

func TestUntrackedReviewCanonicalRejectsUnprovenEpisode(t *testing.T) {
	for _, which := range []string{"wrong_work", "wrong_season", "wrong_number", "wrong_identity", "incomplete_season"} {
		t.Run(which, func(t *testing.T) {
			f := untrackedReviewFixture()
			f.tv()
			f.path("unknown.mkv")
			in := &f.execution.capture.snapshot.Request.Input
			in.TMDBID = 42
			in.CanonicalUnitID = uuid.NewString()
			in.MediaMetadataVersion = 7
			in.UnitMetadataVersion = 9
			in.Season = 1
			in.Episode = 3
			in.TMDBEpisodeID = 403
			switch which {
			case "wrong_work":
				in.TMDBID = 43
			case "wrong_season":
				in.Season = 2
			case "wrong_number":
				in.Episode = 2
			case "wrong_identity":
				in.TMDBEpisodeID = 999
			case "incomplete_season":
				f.metadata.season.Complete = false
			}
			if err := f.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			if f.execution.evidence.Complete || f.execution.evidence.Candidate != nil {
				t.Fatal("invented or mismatched episode accepted")
			}
			untrackedReviewAssertClosed(t, f.files)
		})
	}
}

func TestUntrackedReviewCanonicalPartialProofRejected(t *testing.T) {
	f := untrackedReviewFixture()
	f.tv()
	in := f.execution.capture.snapshot.Request.Input
	in.Episode = 3
	if untrackedReviewInputValid(in) {
		t.Fatal("partial proof accepted")
	}
	in.TMDBID = 42
	in.CanonicalUnitID = uuid.NewString()
	in.MediaMetadataVersion = 7
	in.UnitMetadataVersion = 9
	in.Season = 1
	in.TMDBEpisodeID = 403
	if !untrackedReviewInputValid(in) {
		t.Fatal("complete proof rejected")
	}
	in.MediaType = "movie"
	if untrackedReviewInputValid(in) {
		t.Fatal("movie accepted episode selection")
	}
}
