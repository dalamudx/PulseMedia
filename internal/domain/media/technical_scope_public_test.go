package media

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

func TestTechnicalScopeCannotAuthorizeAppendedUnits(t *testing.T) {
	for _, raw := range []string{
		"[Coalgirls]_Nisekoi_01_(1920x1080_Blu-ray_FLAC)_[F68B03CD].mkv",
		"[EveTaku] Nisekoi 01v2 [720p-Hi10P] [B63D4DBE].mkv",
		"[HorribleSubs] Re Zero kara Hajimeru Isekai Seikatsu - 01A [1080p].mkv",
		"[Jumonji-Giri]_[F-B&A-E]_W_Wish_Ep01_(0c29b4a5).mkv",
		"[Licca] Little Women II - Jo's Boys Episode 4 (VP8)",
		"[Chihiro-Amaterasu]_Rewrite_-_Vol.01_[Blu-ray_720p_Hi10P_FLAC]",
		"[NyaaTorrents] LOVE HINA VOL 5 [DVD-ISO]",
		"Sunabouzu_1-24_[砂ぼうず]_[Desert_Punk]_[moo-shi_eng_softsub][jap_audio][DVD-Rip][H264]",
	} {
		for _, suffix := range []string{"", " / Film (2020) {tmdb-550}.mkv", " / Other.S01E02.mkv", " Other.S01E02.mkv", " Film (2020) {tmdb-550}.mkv", " Film 2020", " - 03 [1080p].mkv"} {
			name := raw + suffix
			t.Run(name, func(t *testing.T) {
				got := RecognizeRelease(name)
				if got.RawName != name || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || got.Season != 0 || len(got.Episodes) != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
					t.Errorf("technical scope retained executable identity: %+v", got)
				}
				legacy := Recognize(name)
				if legacy.RawName != name || !legacy.RequiresReview || legacy.Confidence != 0 || legacy.Identity != nil {
					t.Errorf("legacy identity escaped scope review: %+v", legacy)
				}
				if MatchesAutomaticMovie(name, "Film", 2020, map[string]string{"tmdb": "550"}) {
					t.Error("technical scope authorized movie match")
				}
			})
		}
	}
}

func TestUnsupportedVP8CannotCertifySupportedCodec(t *testing.T) {
	for _, name := range []string{"Film (2020) VP8", "Film (2020) VP8 x264", "Film (2020) x265 VP8"} {
		if got := RecognizeRelease(name); got.Quality.Codec != "" {
			t.Errorf("unsupported codec disappeared: %+v", got)
		}
	}
	if got := RecognizeRelease("Film (2020) VP8x x264"); got.Quality.Codec != quality.CodecH264 {
		t.Errorf("unknown word erased explicit H264: %+v", got)
	}
}
