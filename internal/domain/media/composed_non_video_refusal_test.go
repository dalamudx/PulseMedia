package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
)

func TestComposedScopeViewsPreserveNonVideoRefusal(t *testing.T) {
	for _, raw := range []string{
		"Orchard.S01E01.Season1.COMPLETE.1080p.WEB-DL.x264-GRP.srt",
		"Orchard.Season1.COMPLETE/Other.S02E02.1080p.WEB-DL.x264-GRP.txt",
		"Orchard.2024.SUBPACK.1080p.BluRay.x264-GRP.SRT",
		"Orchard.S00.EXTRAS.2024.1080p.BluRay.x264-GRP.ass",
	} {
		t.Run(raw, func(t *testing.T) {
			r, legacy := media.RecognizeRelease(raw), media.Recognize(raw)
			if r.RawName != raw || r.Reason != "non_video_file" || !r.RequiresReview || r.Confidence != 0 || r.AbsoluteEpisode != 0 || len(r.Episodes) != 0 || len(r.UnitKeys) != 0 {
				t.Fatalf("non-video evidence was replaced by a scope view: %+v", r)
			}
			if legacy.RawName != raw || !legacy.RequiresReview || legacy.Confidence != 0 || legacy.Identity != nil {
				t.Fatalf("non-video legacy result acquired authority: %+v", legacy)
			}
		})
	}
}
