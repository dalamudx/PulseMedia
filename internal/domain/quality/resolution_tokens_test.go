package quality_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"testing"
)

func TestResolutionUnknownForGenericOrContradictoryEvidence(t *testing.T) {
	for _, s := range []string{"HDTV", "TrueHD", "SDR", "Movie HD", "Movie SD", "1080i.HDTV", "4k to 1080p", "4K.Remastered.1080p", "1080p.720p", "720pMkv.Com", "10800p", "蓝1080p字"} {
		if got := quality.ParseResolution(s); got != "" {
			t.Errorf("invented resolution %q -> %s", s, got)
		}
	}
	for _, c := range []struct {
		s string
		r quality.Resolution
	}{{"Film.1080p", quality.Res1080p}, {"[2160p] UHD 4K", quality.Res2160p}, {"[720pMkv.Com]_Film.480p", quality.Res480p}, {"Movie.720P", quality.Res720p}, {"[FHD] Film", quality.Res1080p}} {
		if got := quality.ParseResolution(c.s); got != c.r {
			t.Errorf("explicit resolution lost %q -> %s", c.s, got)
		}
	}
}
