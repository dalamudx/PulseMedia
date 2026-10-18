package media

import "testing"

// These are helper contracts, not golden-record admission or public-parser tests.
func TestUnboundTechnicalEpisodeHintObservedFormats(t *testing.T) {
	cases := []struct {
		name, title, publisher string
	}{
		{"[Coalgirls]_Nisekoi_01_(1920x1080_Blu-ray_FLAC)_[F68B03CD].mkv", "", ""},
		{"[EveTaku] Nisekoi 01v2 [720p-Hi10P] [B63D4DBE].mkv", "", ""},
		{"[HorribleSubs] Re Zero kara Hajimeru Isekai Seikatsu - 01A [1080p].mkv", "", ""},
		{"[Jumonji-Giri]_[F-B&A-E]_W_Wish_Ep01_(0c29b4a5).mkv", "", ""},
		{"[Licca] Little Women II - Jo's Boys Episode 4 (VP8)", "", ""},
		{"[Chihiro-Amaterasu]_Rewrite_-_Vol.01_[Blu-ray_720p_Hi10P_FLAC]", "Rewrite", "Chihiro-Amaterasu"},
		{"[NyaaTorrents] LOVE HINA VOL 5 [DVD-ISO]", "LOVE HINA", ""},
		{"Sunabouzu_1-24_[砂ぼうず]_[Desert_Punk]_[moo-shi_eng_softsub][jap_audio][DVD-Rip][H264]", "Sunabouzu", ""},
		// Structural counterparts prevent a title-specific implementation.
		{"[Coalgirls]_Other_Show_02_(1920x1080_Blu-ray_FLAC)_[ABCDEF12].mkv", "", ""},
		{"[EveTaku] Another Show 03v2 [720p-Hi10P] [1234ABCD].mkv", "", ""},
		{"[HorribleSubs] Another Show - 02B [1080p].mkv", "", ""},
		{"[Jumonji-Giri]_[F-B&A-E]_Another_Show_Ep02_(1234abcd).mkv", "", ""},
		{"[Licca] Another Show Episode 12 (VP8)", "", ""},
		{"[Chihiro-Amaterasu]_Other_Show_-_Vol.02_[Blu-ray_720p_Hi10P_FLAC]", "Other Show", "Chihiro-Amaterasu"},
		{"[NyaaTorrents] ANOTHER SHOW VOL 2 [DVD-ISO]", "ANOTHER SHOW", ""},
		{"Another_Show_2-12_[別名]_[Other_Alias]_[moo-shi_eng_softsub][jap_audio][DVD-Rip][H264]", "Another Show", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{tc.name, "Library/" + tc.name, tc.name + "/Film (2020).mkv", "Library\\" + tc.name + "\\Show.S01E02.mkv", "Show.S01E01 / " + tc.name, tc.name + " / [SubsPlease] Other - 03 [1080p].mkv"} {
				got := unboundTechnicalEpisodeHint(name)
				if got == nil || got.title != tc.title || got.publisher != tc.publisher {
					t.Fatalf("hint(%q) = %+v; want title=%q publisher=%q", name, got, tc.title, tc.publisher)
				}
			}
		})
	}
}

func TestUnboundTechnicalEpisodeHintPreservesOtherFormats(t *testing.T) {
	for _, name := range []string{
		"", "Nisekoi 01.mkv", "[EveTaku] Nisekoi 01.mkv",
		"[EveTaku] Nisekoi 01v2 [720p-Hi10P].mkv",
		"[EveTaku] Nisekoi 01v2 [Unknown] [B63D4DBE].mkv",
		"[EveTaku] Nisekoi 01v2 [720p-Hi10P] [B63D4DB].mkv",
		"[EveTaku] Nisekoi 01v2 [720p-Hi10P] [B63D4DBE] [Unknown].mkv",
		"[Coalgirls]_Nisekoi_01_(Unknown)_[F68B03CD].mkv",
		"[Coalgirls]_Nisekoi_01_(1920x1080_Blu-ray_FLAC)_[F68B03CD].unknown",
		"[HorribleSubs] Series - 01 [1080p].mkv",
		"[HorribleSubs] Series - 01A [Unknown].mkv",
		"[Licca] Series Episode 4 (Unknown)",
		"[Jumonji-Giri]_[F-B&A-E]_W_Wish_Ep01_(not-a-crc).mkv",
		"[SubsPlease] Chihayafuru 3 - 21 [1080p].mkv",
		"[EveTaku] Nisekoi - 01v2 [720p-Hi10P] [B63D4DBE].mkv",
		"[EveTaku] Chihayafuru 3 - 21v2 [720p-Hi10P] [B63D4DBE].mkv",
		"[MTBB] Series - 123 [ABCDEF12].mkv",
		"[Coalgirls]_Film_2020_(1920x1080_Blu-ray_FLAC)_[F68B03CD].mkv",
		"[EveTaku] Film 2020 [720p-Hi10P] [B63D4DBE].mkv",
		"[Chihiro-Amaterasu]_Movie_Vol.3_(2020)_[Blu-ray_720p_Hi10P_FLAC].mkv",
		"[Chihiro-Amaterasu]_Movie_2020_-_Vol.3_[Blu-ray_720p_Hi10P_FLAC]",
		"[NyaaTorrents] Movie Vol.3 (2020) [DVD-ISO]",
		"[NyaaTorrents] Movie 2020 VOL 3 [DVD-ISO]",
		"Movie Vol.3 (2020) 1080p.mkv", "Movie (2020) [Unknown].mkv",
		"[Unknown] Rewrite Vol.01 [Unknown]",
		"[Unknown] Rewrite_-_Vol.01_[Blu-ray_720p_Hi10P_FLAC]",
		"[Chihiro-Amaterasu]_Rewrite_-_Vol.01_[Unknown]",
		"[NyaaTorrents] LOVE HINA VOL 5 [Unknown]",
		"Sunabouzu_1-24_[Unknown]_[H264]",
		"Sunabouzu_1-24_[砂ぼうず]_[Desert_Punk]_[Unknown][jap_audio][DVD-Rip][H264]",
		"Library/S01/Show.S01E01.1080p.mkv",
		"Library/S01E02/Film (2020).mkv",
		"Library/1x02/Film (2020).mkv",
		"Library/2020/Film (2020).mkv",
		"Library\\S01E02\\Film (2020).mkv",
		"[EveTaku] Series 01v2 [720p-Hi10P] [B63D4DBE\n].mkv",
	} {
		t.Run(name, func(t *testing.T) {
			if got := unboundTechnicalEpisodeHint(name); got != nil {
				t.Fatalf("hint(%q) = %+v; want nil", name, got)
			}
		})
	}
}
