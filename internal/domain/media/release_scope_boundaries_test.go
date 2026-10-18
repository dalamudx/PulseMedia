package media

import "testing"

func TestRLSRecoveryRawScopeBothAPIs(t *testing.T) {
	inputs := []string{
		"Scope.E23.1080p.WEB-DL.ID123/Child.S01E01.mkv",
		"Scope.S01E01.3xDVD9.NTSC.1080p.WEB-DL",
		"[Unknown] Scope.E23.1080p.WEB-DL.ID123/Child.S01E01.mkv",
		"Scope.S01 E01.1080p.WEB-DL.x264-GRP",
		"Scope.S01 - Episode 1.1080p.WEB-DL.x264-GRP",
		"Scope.S01E00001.1080p.WEB-DL.x264-GRP",
		"Scope.2024.Ep.01-03.Complete.1080p.WEB-DL/Child.2024.mkv",
		"Scope/Ep.01-03.Complete/Child.S01E01.1080p.WEB-DL.mkv",
		"[Unknown] Scope.S01-S02.COMPLETE/Child.S01E01.1080p.WEB-DL.mkv",
		"Scope.2020.2024.S01-S02.COMPLETE.ID123/Child.2024.mkv",
		"Scope.S01-S02.COMPLETE.ID123.S01E01.1x02.1080p.WEB-DL",
		"Scope.S01 E01.ID123/Child.S01E02.1080p.WEB-DL.mkv",
	}
	for _, raw := range inputs {
		t.Run(raw, func(t *testing.T) {
			r, m := RecognizeRelease(raw), Recognize(raw)
			if r.Confidence != 0 || !r.RequiresReview || len(r.UnitKeys) != 0 || len(r.Episodes) != 0 || r.AbsoluteEpisode != 0 {
				t.Fatalf("release authority: %+v", r)
			}
			if m.Identity != nil || m.Confidence != 0 || !m.RequiresReview {
				t.Fatalf("movie authority: %+v", m)
			}
			if r.RawName != raw || m.RawName != raw {
				t.Fatal("raw input changed")
			}
		})
	}
}
func TestRLSRecoveryDisplayAndPositiveControls(t *testing.T) {
	for _, raw := range []string{"Aka.Ordinary.Movie.2024.1080p.WEB-DL.x264-GRP", "Movie.AKA.Other.2024.1080p.WEB-DL.x264-GRP", "[Unknown] Movie.2024.1080p.WEB-DL.x264-GRP", "Movie.2024.1080p.WEB-DL.x264-[Unknown]"} {
		r := recognizeReleaseEvidence(raw)
		m := recognizeFilenameEvidence(raw)
		if rlsNextTitle(raw, r.Title, r.Year) != r.Title || rlsNextTitle(raw, m.Title, m.Year) != m.Title {
			t.Fatalf("ambiguous title stripped: %s", raw)
		}
	}
	r := RecognizeRelease("Scope.S01E01.1080p.WEB-DL.x264-GRP")
	if r.Confidence == 0 || r.RequiresReview || len(r.UnitKeys) != 1 || r.UnitKeys[0] != "S01E01" {
		t.Fatal(r)
	}
	m := Recognize("Scope.2024.1080p.WEB-DL.x264-GRP")
	if m.Confidence != 0.9 || m.RequiresReview {
		t.Fatal(m)
	}
	for _, raw := range []string{"Scope.S01E01.1080p.WEB-DL.srt", "Scope.2024.1080p.WEB-DL.nfo"} {
		r := RecognizeRelease(raw)
		if len(r.UnitKeys) > 0 || r.Confidence != 0 {
			t.Fatal(r)
		}
	}
}

func TestRLSRecoveryPublisherTechnicalTail(t *testing.T) {
	for _, tt := range []struct{ suffix, group string }{
		{"HDMA_x264", ""},
		{"DTS_x265", ""},
		{"H264_Team", ""},
		{"North_Studio", "North_Studio"},
		{"North_x264bar", "North_x264bar"},
	} {
		raw := "Unseen.2024.1080p.BluRay.x264-" + tt.suffix + ".mkv"
		t.Run(tt.suffix, func(t *testing.T) {
			movie, release := Recognize(raw), RecognizeRelease(raw)
			if movie.Quality.Group != tt.group || release.Quality.Group != tt.group {
				t.Fatalf("technical tail or publisher mismatch: %+v / %+v", movie.Quality, release.Quality)
			}
			if movie.RawName != raw || release.RawName != raw || movie.Year != 2024 || release.Year != 2024 || len(release.UnitKeys) != 1 || release.UnitKeys[0] != "movie" {
				t.Fatalf("group boundary changed title-year coverage: %+v / %+v", movie, release)
			}
		})
	}
}
