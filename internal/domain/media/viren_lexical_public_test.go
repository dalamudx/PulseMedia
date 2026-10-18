package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"strings"
	"testing"
)

func TestVirenUnseenLexical(t *testing.T) {
	for _, x := range []struct{ raw, title string }{
		{"Незнакомая комета[torrents.ru].avi", "Незнакомая комета"},
		{"Unseen Comet _ Adult Swim.mp4", "Unseen Comet"},
		{"Unseen.Comet.INTEGRAL.MULTI.COMPLETE.BLURAY-GRP", "Unseen Comet"},
		{"[Lazyleido-Mini] Unseen Comet - 08 (S01E08) - (WEB 1080p AV1 10-bit AAC 2.0) [A1B2C3D4]", "Unseen Comet"},
		{"[www.arabp2p.net]_-_تركي مترجم ومدبلج Unseen.Comet.2024.1080p.WEB-DL.x264.mkv.torrent", "Unseen Comet"},
		{"Unseen.Comet.CUSTOM.EXTENDED.2024.1080p.BluRay.x264-GRP.mkv", "Unseen Comet"},
		{"RING.123.PPV.First.vs.Second.HDTV.x264-GRP[TGx]", "RING 123 First vs Second"},
		{"Unseen Comet S3 OVA - 07.mkv", "Unseen Comet"},
		{"[Anipakku] Unseen Comet - Season 3 46.mkv", "Unseen Comet"},
		{"Erai-raws-Unseen_Comet_Season_3-01-720p_CR_WEB-DL_AVC_AAC-MultiSub-A1B2C3D4.mkv", "Unseen Comet"},
		{"[Erai-raws] Unseen Comet Season 3 - 14 [1080p CR WEBRip HEVC AAC][MultiSub][A1B2C3D4]", "Unseen Comet"},
		{"[LostYears] Unseen op.Comet (WEB 1080p x264 AAC) [Dual-Audio]", "Unseen op.Comet"},
	} {
		t.Run(x.raw, func(t *testing.T) {
			r, m := media.RecognizeRelease(x.raw), media.Recognize(x.raw)
			if r.Title != x.title || m.Title != x.title || r.RawName != x.raw || m.RawName != x.raw {
				t.Fatalf("public title mismatch release=%+v movie=%+v", r, m)
			}
		})
	}
}
func TestVirenNormalTitleControls(t *testing.T) {
	for _, raw := range []string{
		"Unseen CUSTOM EXTENDED Story.2024.1080p.BluRay.x264.mkv",
		"Unseen.CUSTOM.EXTENDED.2024.1080p.Unlisted.x264.mkv",
		"Unseen.PPV.Story.2024.1080p.WEB-DL.mkv",
	} {
		r, m := media.RecognizeRelease(raw), media.Recognize(raw)
		word := "PPV"
		if strings.Contains(raw, "EXTENDED") {
			word = "CUSTOM EXTENDED"
		}
		if !strings.Contains(r.Title, word) || !strings.Contains(m.Title, word) {
			t.Fatalf("ordinary title erased %q: %q / %q", raw, r.Title, m.Title)
		}
	}
}
