package media_test

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
)

func automaticTVFileTarget() media.AutomaticTVTorrentTarget {
	return media.AutomaticTVTorrentTarget{
		Unit:  media.NumberingUnit{ID: uuid.MustParse("7e7c440e-5fd4-4f7b-955f-ad6687bd1261"), EpisodeID: 1003, Season: 1, Episode: 3, Key: "S01E03", Version: 7},
		Title: "Fixture", Year: 2020, TMDBID: 100,
	}
}

func TestAutomaticTVSingleEpisodeFilesExactFrozenBinding(t *testing.T) {
	target := automaticTVFileTarget()
	for _, prefix := range []string{"", "Fixture/", "Fixture.2020/", "Fixture.2020.S01E03/"} {
		t.Run(prefix+"single video", func(t *testing.T) {
			files := []media.TVTorrentFile{{Path: prefix + "Fixture.2020.S01E03.mkv", Size: 7}}
			got, err := media.ValidateAutomaticTVSingleEpisodeFiles(files, target)
			want := &media.TVTorrentUnitFile{Path: files[0].Path, Size: 7, Unit: media.NumberingRow{SourceNumber: 3, EpisodeID: 1003, UnitID: target.Unit.ID, UnitKey: "S01E03", UnitVersion: 7}}
			if err != nil || got == nil || *got != *want {
				t.Fatalf("exact single frozen unit rejected: %v, %#v", err, got)
			}
		})
	}
}

func TestAutomaticTVSingleEpisodeFilesSidecarsAndVideoExtensions(t *testing.T) {
	for _, ext := range []string{".mkv", ".mp4", ".m4v", ".avi", ".mov", ".wmv", ".webm", ".ts", ".m2ts"} {
		t.Run(ext, func(t *testing.T) {
			files := []media.TVTorrentFile{{Path: "Fixture/Fixture.2020.S01E03" + ext, Size: 7}}
			for i, sidecar := range []string{".srt", ".ass", ".ssa", ".sub", ".idx", ".nfo", ".txt", ".jpg", ".jpeg", ".png"} {
				files = append(files, media.TVTorrentFile{Path: "Fixture/extra" + sidecar, Size: int64(i % 2)})
			}
			got, err := media.ValidateAutomaticTVSingleEpisodeFiles(files, automaticTVFileTarget())
			if err != nil || got == nil || got.Path != files[0].Path || got.Size != 7 {
				t.Fatal("safe video and sidecars rejected", err)
			}
		})
	}
}

func TestAutomaticTVSingleEpisodeFilesLocalizedOriginalAndSparseUnits(t *testing.T) {
	for _, name := range []string{"狂飙.2023.S01E03.mkv", "The.Knockout.2023.S01E03.mkv", "The.Knockout.2023.S01E03.{tmdb-100}.mkv"} {
		t.Run(name, func(t *testing.T) {
			target := automaticTVFileTarget()
			target.Title, target.OriginalTitle, target.Year = "狂飙", "The Knockout", 2023
			got, err := media.ValidateAutomaticTVSingleEpisodeFiles([]media.TVTorrentFile{{Path: name, Size: 7}}, target)
			if err != nil || got == nil || got.Unit.UnitID != target.Unit.ID {
				t.Fatal("trusted alias rejected", err)
			}
		})
	}
	target := automaticTVFileTarget()
	target.Unit.Episode, target.Unit.Key = 1056, "S01E1056"
	got, err := media.ValidateAutomaticTVSingleEpisodeFiles([]media.TVTorrentFile{{Path: "Fixture.2020.S01E1056.mkv", Size: 7}}, target)
	if err != nil || got == nil || got.Unit.SourceNumber != 1056 {
		t.Fatal("single sparse target required a season snapshot", err)
	}
}

func TestAutomaticTVSingleEpisodeFilesRejectsUntrustedNames(t *testing.T) {
	for _, name := range []string{
		"Other.2020.S01E03.mkv", "Fixture.2021.S01E03.mkv", "Fixture.2020.S01E04.mkv", "Fixture.2020.S02E03.mkv",
		"Fixture.2020.S01E03.{tmdb-999}.mkv", "Fixture.2020.S01E03.{tmdb-100}.{tmdb-999}.mkv", "Fixture.2020.S01E03.tt1234567.mkv",
		"Fixture.S01E03.mkv", "S01E03.mkv", "2020.S01E03.mkv", "Fixture.2020.mkv", "Fixture.2020.S00E03.mkv",
		"Fixture.2020.1x03.mkv", "[Team] Fixture.2020 - 03 [1080p].mkv", "Fixture.2020.E03.mkv",
		"Fixture.2020.S01E01-E03.mkv", "Fixture.2020.S01E03-E03.mkv", "Fixture.2020.S01E03E04.mkv", "Fixture.2020.S01E03.S01E03.mkv",
		"Fixture.2020.S01.COMPLETE.mkv", "Fixture.2020.COMPLETE.S01E03.mkv", "Fixture.2020.S01E03.part1.mkv", "Fixture.2020.S01E03.sample.mkv",
		"Fixture.2020.S01E03.2021.mkv", "Fixture.2020.S01E03.mkv.rar", "Fixture.2020.S01E03.exe", "Fixture.2020.S01E03.mkv\u200b",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := media.ValidateAutomaticTVSingleEpisodeFiles([]media.TVTorrentFile{{Path: name, Size: 7}}, automaticTVFileTarget())
			if got != nil || !errors.Is(err, media.ErrAutomaticTVTorrentFilesReview) || err.Error() != media.ErrAutomaticTVTorrentFilesReview.Error() {
				t.Fatal("name accepted or unsafe error")
			}
		})
	}
}

func TestAutomaticTVSingleEpisodeFilesRejectsInvalidTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*media.AutomaticTVTorrentTarget)
	}{
		{"zero unit UUID", func(v *media.AutomaticTVTorrentTarget) { v.Unit.ID = uuid.Nil }},
		{"old version", func(v *media.AutomaticTVTorrentTarget) { v.Unit.Version = 0 }},
		{"negative version", func(v *media.AutomaticTVTorrentTarget) { v.Unit.Version = -1 }},
		{"missing provider episode", func(v *media.AutomaticTVTorrentTarget) { v.Unit.EpisodeID = 0 }},
		{"negative provider episode", func(v *media.AutomaticTVTorrentTarget) { v.Unit.EpisodeID = -1 }},
		{"retired", func(v *media.AutomaticTVTorrentTarget) { v.Unit.Retired = true }},
		{"special", func(v *media.AutomaticTVTorrentTarget) { v.Unit.Season = 0; v.Unit.Key = "S00E03" }},
		{"season too large", func(v *media.AutomaticTVTorrentTarget) { v.Unit.Season = 1000 }},
		{"missing episode", func(v *media.AutomaticTVTorrentTarget) { v.Unit.Episode = 0 }},
		{"episode too large", func(v *media.AutomaticTVTorrentTarget) { v.Unit.Episode = 10000 }},
		{"noncanonical key", func(v *media.AutomaticTVTorrentTarget) { v.Unit.Key = "S1E3" }},
		{"wrong key", func(v *media.AutomaticTVTorrentTarget) { v.Unit.Key = "S01E04" }},
		{"missing series", func(v *media.AutomaticTVTorrentTarget) { v.TMDBID = 0 }},
		{"negative series", func(v *media.AutomaticTVTorrentTarget) { v.TMDBID = -1 }},
		{"missing year", func(v *media.AutomaticTVTorrentTarget) { v.Year = 0 }},
		{"year too large", func(v *media.AutomaticTVTorrentTarget) { v.Year = 10000 }},
		{"missing title", func(v *media.AutomaticTVTorrentTarget) { v.Title = "" }},
		{"wrong catalog title", func(v *media.AutomaticTVTorrentTarget) { v.Title = "Other" }},
		{"wrong catalog year", func(v *media.AutomaticTVTorrentTarget) { v.Year = 2021 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := automaticTVFileTarget()
			tc.mutate(&v)
			got, err := media.ValidateAutomaticTVSingleEpisodeFiles([]media.TVTorrentFile{{Path: "Fixture.2020.S01E03.mkv", Size: 7}}, v)
			if got != nil || err != media.ErrAutomaticTVTorrentFilesReview {
				t.Fatal("invalid target accepted")
			}
		})
	}
}

func TestAutomaticTVSingleEpisodeFilesRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../Fixture.2020.S01E03.mkv", "/Fixture.2020.S01E03.mkv", "./Fixture.2020.S01E03.mkv", "Fixture//Fixture.2020.S01E03.mkv", "Fixture/../Fixture.2020.S01E03.mkv", "C:Fixture.2020.S01E03.mkv", `Fixture\Fixture.2020.S01E03.mkv`, "Fixture/Fixture.2020.S01E03.mkv/", "Fixture.2020.S01E03\x00.mkv", "Fixture.2020.S01E03\n.mkv", "Fixture.2020.S01E03\u0085.mkv", "Fixture.2020.S01E03\xff.mkv", strings.Repeat("x", 4097) + ".mkv"} {
		t.Run(fmt.Sprintf("path %q", name[:min(len(name), 50)]), func(t *testing.T) {
			got, err := media.ValidateAutomaticTVSingleEpisodeFiles([]media.TVTorrentFile{{Path: name, Size: 7}}, automaticTVFileTarget())
			if got != nil || err != media.ErrAutomaticTVTorrentFilesReview {
				t.Fatal("unsafe path accepted")
			}
		})
	}
}

func TestAutomaticTVSingleEpisodeFilesRejectsFileSets(t *testing.T) {
	video := media.TVTorrentFile{Path: "Fixture.2020.S01E03.mkv", Size: 7}
	for _, tc := range []struct {
		name  string
		files []media.TVTorrentFile
	}{
		{"nil", nil}, {"empty", []media.TVTorrentFile{}},
		{"too many", make([]media.TVTorrentFile, 4097)},
		{"sidecars only", []media.TVTorrentFile{{Path: "poster.jpg", Size: 7}}},
		{"zero video", []media.TVTorrentFile{{Path: video.Path}}},
		{"negative video", []media.TVTorrentFile{{Path: video.Path, Size: -1}}},
		{"negative sidecar", []media.TVTorrentFile{video, {Path: "readme.txt", Size: -1}}},
		{"overflow", []media.TVTorrentFile{{Path: video.Path, Size: math.MaxInt64}, {Path: "readme.txt", Size: 1}}},
		{"duplicate", []media.TVTorrentFile{video, video}},
		{"duplicate sidecar case", []media.TVTorrentFile{video, {Path: "poster.JPG"}, {Path: "POSTER.jpg"}}},
		{"duplicate Unicode fold", []media.TVTorrentFile{video, {Path: "s.srt"}, {Path: "ſ.srt"}}},
		{"file prefix", []media.TVTorrentFile{video, {Path: "Fixture.jpg"}, {Path: "Fixture.jpg/poster.png"}}},
		{"case prefix", []media.TVTorrentFile{video, {Path: "FIXTURE.jpg"}, {Path: "fixture.JPG/poster.png"}}},
		{"second episode", []media.TVTorrentFile{video, {Path: "Fixture.2020.S01E04.mkv", Size: 7}}},
		{"same episode second video", []media.TVTorrentFile{video, {Path: "Fixture.2020.S01E03.mp4", Size: 7}}},
		{"sample extra", []media.TVTorrentFile{video, {Path: "sample.mkv", Size: 1}}},
		{"unknown extra", []media.TVTorrentFile{video, {Path: "readme.html"}}},
		{"archive extra", []media.TVTorrentFile{video, {Path: "extra.zip"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := media.ValidateAutomaticTVSingleEpisodeFiles(tc.files, automaticTVFileTarget())
			if got != nil || err != media.ErrAutomaticTVTorrentFilesReview {
				t.Fatal("unsafe file set accepted")
			}
		})
	}
}

func TestAutomaticTVSingleEpisodeFilesDirectoryContradictions(t *testing.T) {
	for _, parent := range []string{"Other", "Fixture.2021", "Other.2020", "Fixture.2020.{tmdb-999}", "Fixture.2020.S01E04", "Other.2020.S01E03", "Fixture.2021.S01E03", "Fixture.S01E03", "S01E03", "S01E04", "Fixture.2020.S00E03", "Fixture.2020.1x03", "Fixture.2020.S01E01-E03", "Fixture.2020.S01.COMPLETE", "Fixture.2020.S01E03.part1", "[Team] Fixture.2020 - 03 [1080p]", "sample", "Fixture.2020.S01E03/Other.2020.S01E03"} {
		t.Run(parent, func(t *testing.T) {
			got, err := media.ValidateAutomaticTVSingleEpisodeFiles([]media.TVTorrentFile{{Path: parent + "/Fixture.2020.S01E03.mkv", Size: 7}}, automaticTVFileTarget())
			if got != nil || err != media.ErrAutomaticTVTorrentFilesReview {
				t.Fatal("contradictory parent accepted")
			}
		})
	}
	// A truthful parent cannot repair unproven filename identity.
	got, err := media.ValidateAutomaticTVSingleEpisodeFiles([]media.TVTorrentFile{{Path: "Fixture.2020.S01E03/S01E03.mkv", Size: 7}}, automaticTVFileTarget())
	if got != nil || err != media.ErrAutomaticTVTorrentFilesReview {
		t.Fatal("parent supplied filename identity")
	}
}

func TestAutomaticTVSingleEpisodeFilesNoMutationAndBounds(t *testing.T) {
	target := automaticTVFileTarget()
	beforeTarget := target
	files := []media.TVTorrentFile{{Path: "Fixture.2020.S01E03.mkv", Size: math.MaxInt64}}
	for i := 1; i < 4096; i++ {
		files = append(files, media.TVTorrentFile{Path: fmt.Sprintf("extra%d.txt", i)})
	}
	before := append([]media.TVTorrentFile(nil), files...)
	got, err := media.ValidateAutomaticTVSingleEpisodeFiles(files, target)
	if err != nil || got == nil || got.Size != math.MaxInt64 {
		t.Fatal("inclusive file/size bounds rejected", err)
	}
	got.Path = "mutated"
	got.Size = 0
	got.Unit.UnitVersion++
	if !reflect.DeepEqual(files, before) || target != beforeTarget {
		t.Fatal("success mutated caller inputs")
	}
	files[4095].Path = "../private-passkey.txt"
	before = append([]media.TVTorrentFile(nil), files...)
	if v, e := media.ValidateAutomaticTVSingleEpisodeFiles(files, target); v != nil || e != media.ErrAutomaticTVTorrentFilesReview {
		t.Fatal("invalid path accepted")
	}
	if !reflect.DeepEqual(files, before) || target != beforeTarget {
		t.Fatal("failure mutated caller inputs")
	}
}
