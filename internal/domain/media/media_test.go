package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestMediaIdentity(t *testing.T) {
	id, err := media.ParseIdentity("tmdb.movie:550")
	if err != nil {
		t.Fatalf("failed to parse identity: %v", err)
	}
	if id.Namespace != "tmdb.movie" || id.ExternalID != "550" {
		t.Fatalf("unexpected parsed identity: %+v", id)
	}
	if id.String() != "tmdb.movie:550" {
		t.Fatalf("unexpected string output: %s", id.String())
	}

	// Invalid format
	if _, err := media.ParseIdentity("invalid"); err == nil {
		t.Fatal("expected error for identity without colon")
	}
}

func TestUnitKeys(t *testing.T) {
	if media.FormatMovieUnitKey() != "movie" {
		t.Fatalf("unexpected movie key")
	}
	if media.FormatEpisodeUnitKey(1, 3) != "S01E03" {
		t.Fatalf("unexpected episode key")
	}
}
