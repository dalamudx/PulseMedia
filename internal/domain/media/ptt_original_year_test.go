package media_test

import (
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"testing"
)

// Independent original MIT annotations, not recognizer-generated expectations:
// dreulavelle/PTT@88429bb90acef55673f421f45038878809b1e577 tests/test_year.py.
// LICENSE hash is pinned by testdata/releases/upstream-mit/manifest.json.
// This is a partial year-safety oracle, NOT complete golden approval.
func TestOriginalMITYearEvidence(t *testing.T) {
	for _, c := range []struct {
		input string
		year  int
	}{{"Dawn.of.the.Planet.of.the.Apes.2014.HDRip.XViD-EVO", 2014}, {"Hercules (2014) 1080p BrRip H264 - YIFY", 2014}, {"One Shot [2014] DVDRip XViD-ViCKY", 2014}, {"2012 2009 1080p BluRay x264 REPACK-METiS", 2009}, {"2008 The Incredible Hulk Feature Film.mp4", 2008}, {"Harry Potter All Movies Collection 2001-2011 720p Dual KartiKing", 0}, {"Empty Nest Season 1 (1988 - 89) fiveofseven", 0}, {"04. Practice Two (1324mb 1916x1080 50fps 1970kbps x265 deef).mkv", 0}, {"Anatomia De Grey - Temporada 19 [HDTV][Cap.1905][Castellano][www.AtomoHD.nu].avi", 0}, {"Wonder Woman 1984 (2020) [UHDRemux 2160p DoVi P8 Es-DTSHD AC3 En-AC3].mkv", 2020}, {"1923 S02E01 The Killing Season 1080p AMZN WEB-DL DDP5 1 H 264-FLUX[TGx]", 0}, {"1883 - Season 1 (S01) (A Yellowstone Origin Story) [2160p NVEnc 10Bit HVEC][DDP 5.1Ch][WEBRip][English Subs]", 0}, {"1883.S01E01.1883.2160p.WEB-DL.DDP5.1.H.265-NTb.mkv", 0}} {
		t.Run(c.input, func(t *testing.T) {
			r := media.RecognizeRelease(c.input)
			if r.RequiresReview {
				if len(r.UnitKeys) != 0 || (c.year == 0 && r.Year != 0) {
					t.Fatal("ambiguous year still granted authority", r)
				}
				return
			}
			if r.Year != c.year {
				t.Fatal("independent year evidence contradicted", c.year, r)
			}
		})
	}
}
