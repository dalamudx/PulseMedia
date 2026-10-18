package naming_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestFormatMovieFile(t *testing.T) {
	q := quality.Profile{
		Source:     quality.SourceBluray,
		Resolution: quality.Res1080p,
		Codec:      quality.CodecHEVC,
	}

	formatted := naming.FormatMovieFile("Fight Club", 1999, q, "mkv")
	expected := "Fight Club (1999) [Bluray 1080p HEVC].mkv"
	if formatted != expected {
		t.Fatalf("expected %q, got %q", expected, formatted)
	}

	dir := naming.FormatMovieDir("Fight Club", 1999)
	if dir != "Fight Club (1999)" {
		t.Fatalf("expected %q, got %q", "Fight Club (1999)", dir)
	}
}

func TestCleanRelPath(t *testing.T) {
	// Normal safe path
	safe, err := naming.CleanRelPath("movies/sub/file.mkv")
	if err != nil || safe != "movies/sub/file.mkv" {
		t.Fatalf("expected safe path, got: %s (err: %v)", safe, err)
	}

	// Directory traversal must be rejected
	if _, err := naming.CleanRelPath("../../../etc/passwd"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
	if _, err := naming.CleanRelPath("/abs/path/file.mkv"); err == nil {
		t.Fatal("expected absolute path to be rejected")
	}
}
