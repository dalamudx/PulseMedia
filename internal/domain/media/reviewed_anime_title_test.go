package media

import "testing"

func TestReviewedAnimeTitleViewObservedLayouts(t *testing.T) {
	// Literal title labels from the pinned MIT Rakun source, not parser output.
	for _, row := range []struct{ input, title string }{
		{"[Ohys-Raws] Code Geass Fukkatsu no Lelouch (BD 1920x1080 x264 FLACx2).mkv", "Code Geass Fukkatsu no Lelouch"},
		{"[Teke]_Made in Abyss_BDRip_AAC_1080MP4", "Made in Abyss"},
		{"[Cleo] Kimi no Na wa | Your Name [Dual Audio 10bit BD720p][HEVC-x265]", "Kimi no Na wa | Your Name"},
		{"[pcela-anime] Kimi no Na wa [BRrip 1920x1080 x264 AAC] [SUB PL+EN]", "Kimi no Na wa"},
		{"[Cervoz] Serial Experiments Lain [1080p][MULTI][VF - VOSTFR][10Bits]", "Serial Experiments Lain"},
		{"[JpU - No oficial] Serial Experiments Lain", "Serial Experiments Lain"},
		{"[Reaktor] From the New World - Shinsekai Yori [1080p][BD][x265][10-bit][Dual-Audio]", "From the New World - Shinsekai Yori"},
		{"[UQW] Fullmetal Alchemist: Brotherhood [BD 1080p AVC-YUV420P10 FLAC]", "Fullmetal Alchemist: Brotherhood"},
		{"[matheousse] Death Note MULTi VF/VOSTFR (BD 1080p AAC)", "Death Note"},
		{"[SOFCJ-Raws] Death Note (BDRip 1920x1080 x264 10bit VFR FLAC)", "Death Note"},
		{"[Hi10] Kateikyoushi Hitman Reborn [DVD 576p]", "Kateikyoushi Hitman Reborn"},
	} {
		t.Run(row.input, func(t *testing.T) {
			if got, ok := reviewedAnimeTitleView(row.input); !ok || got != row.title {
				t.Fatalf("title = %q, %v; want %q", got, ok, row.title)
			}
		})
	}
}

func TestReviewedAnimeTitleViewStructuralTitles(t *testing.T) {
	// Same observed layouts with a different ordinary title. None of these words
	// or years may become a technical boundary or inferred identity/coverage.
	const title = "Web Cam German 1998: Another Story - A New World | Part Two"
	for _, layout := range []struct{ prefix, tail string }{
		{"[Ohys-Raws] ", " (BD 1920x1080 x264 FLACx2).mkv"},
		{"[Teke]_", "_BDRip_AAC_1080MP4"},
		{"[Cleo] ", " [Dual Audio 10bit BD720p][HEVC-x265]"},
		{"[pcela-anime] ", " [BRrip 1920x1080 x264 AAC] [SUB PL+EN]"},
		{"[Cervoz] ", " [1080p][MULTI][VF - VOSTFR][10Bits]"},
		{"[JpU - No oficial] ", ""},
		{"[Reaktor] ", " [1080p][BD][x265][10-bit][Dual-Audio]"},
		{"[UQW] ", " [BD 1080p AVC-YUV420P10 FLAC]"},
		{"[matheousse] ", " MULTi VF/VOSTFR (BD 1080p AAC)"},
		{"[SOFCJ-Raws] ", " (BDRip 1920x1080 x264 10bit VFR FLAC)"},
		{"[Hi10] ", " [DVD 576p]"},
	} {
		input := layout.prefix + title + layout.tail
		if got, ok := reviewedAnimeTitleView(input); !ok || got != title {
			t.Errorf("%q => %q, %v; want intact %q", input, got, ok, title)
		}
	}
}

func TestReviewedAnimeTitleViewRetainsCoverageWords(t *testing.T) {
	for _, title := range []string{
		"Ordinary S01E01", "Ordinary - 25", "Ordinary #05", "Ordinary 12",
		"Ordinary EP1-EP12", "Ordinary OVA", "Ordinary Complete",
		"Ordinary Web Cam German (1998)", "Ordinary (Unknown Continuation)",
	} {
		if got, ok := reviewedAnimeTitleView("[Hi10] " + title + " [DVD 576p]"); !ok || got != title {
			t.Errorf("title evidence lost: %q => %q, %v", title, got, ok)
		}
	}
}

func TestReviewedAnimeTitleViewDeclinesUnknownEvidence(t *testing.T) {
	for _, input := range []string{
		"[UnknownPublisher] Ordinary Title [DVD 576p]",
		"[Hi10Extra] Ordinary Title [DVD 576p]",
		"prefix [Hi10] Ordinary Title [DVD 576p]",
		"/library/[Hi10] Ordinary Title [DVD 576p]",
		"C:\\library\\[Hi10] Ordinary Title [DVD 576p]",
		"[Hi10] [UnknownPrefix] Ordinary Title [DVD 576p]",
		"[Hi10] Ordinary [UnknownTitle] [DVD 576p]",
		"[Hi10] Ordinary Title [DVD 576p][UnknownTail]",
		"[Hi10] Ordinary Title [DVD 576p] Continued Story",
		"[Hi10] Ordinary Title [DVD 576p Unknown]",
		"[Hi10] Ordinary Title [DVD 576p",
		"[Hi10] Ordinary Title DVD 576p",
		"[Hi10] Ordinary Title [DVD 480p]",
		"[Hi10] Ordinary Title [DVD 576p].unknown",
		"[Hi10] Ordinary/Other Title [DVD 576p]",
		"[Hi10] [DVD 576p]",
		"[Hi10]Ordinary Title [DVD 576p]",
		"[Hi10]\nOrdinary Title [DVD 576p]",
		"[JpU - No oficial] Ordinary [UnknownTitle]",
		"[JpU - No oficial] Ordinary {UnknownTitle}",
		"[UnknownPublisher] Web Cam German 1998",
		"Web Cam German 1998",
	} {
		if got, ok := reviewedAnimeTitleView(input); ok || got != "" {
			t.Errorf("unreviewed layout repaired: %q => %q, %v", input, got, ok)
		}
	}
}
