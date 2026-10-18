package naming

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"strings"
	"testing"
)

func TestEpisodeRelCanonicalNoGuess(t *testing.T) {
	p, e := FormatEpisodeRel("Fixture Show", 2020, 2, 3, quality.Profile{Resolution: quality.Res1080p}, ".mkv")
	if e != nil || p != "Fixture Show (2020)/Season 02/Fixture Show - S02E03 [1080p].mkv" {
		t.Fatal(p, e)
	}
	p, e = FormatEpisodeRel("Fixture", 0, 0, 1, quality.Profile{}, ".mkv")
	if e != nil || !strings.Contains(p, "S00E01") {
		t.Fatal("explicit special lost", p, e)
	}
	for _, c := range []struct {
		title string
		s, e  int
		ext   string
	}{{"", 1, 1, ".mkv"}, {"..", 1, 1, ".mkv"}, {"Fixture", -1, 1, ".mkv"}, {"Fixture", 1, 0, ".mkv"}, {"Fixture", 1, 1, "/escape"}, {"Fixture\n", 1, 1, ".mkv"}} {
		if p, e := FormatEpisodeRel(c.title, 0, c.s, c.e, quality.Profile{}, c.ext); e == nil {
			t.Fatal("unknown numbering/path accepted", p, c)
		}
	}
}
