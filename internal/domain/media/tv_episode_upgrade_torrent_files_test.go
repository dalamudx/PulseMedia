package media_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/google/uuid"
)

func tvEpisodeUpgradeTarget() media.TVEpisodeUpgradeTarget {
	return media.TVEpisodeUpgradeTarget{Unit: media.NumberingUnit{ID: uuid.MustParse("7e7c440e-5fd4-4f7b-955f-ad6687bd1261"), EpisodeID: 1003, Season: 1, Episode: 3, Key: "S01E03", Version: 7}, Title: "Fixture", Year: 2020, TMDBID: 100}
}

func TestTVEpisodeUpgradeTorrentFilesExactBinding(t *testing.T) {
	target := tvEpisodeUpgradeTarget()
	for _, prefix := range []string{"", "Fixture/", "Fixture.2020/", "Fixture.2020.S01E03/"} {
		files := []media.TVTorrentFile{{Path: prefix + "Fixture.2020.S01E03.1080p.WEB-DL.mkv", Size: 7}, {Path: prefix + "Fixture.2020.S01E03.1080p.WEB-DL.zh.default.srt"}, {Path: prefix + "poster.jpg"}, {Path: prefix + "readme.txt"}}
		before := append([]media.TVTorrentFile(nil), files...)
		got, err := media.ValidateTVEpisodeUpgradeTorrentFiles(files, target)
		if err != nil || got == nil || got.Path != files[0].Path || got.Size != 7 || got.Unit.UnitID != target.Unit.ID || got.Unit.UnitVersion != 7 || got.Unit.EpisodeID != 1003 {
			t.Fatalf("exact binding: %#v, %v", got, err)
		}
		got.Path = "changed"
		if !reflect.DeepEqual(files, before) || target != tvEpisodeUpgradeTarget() {
			t.Fatal("mutated input")
		}
	}
	target.Title, target.OriginalTitle, target.Year = "狂飙", "The Knockout", 2023
	target.Unit.Episode, target.Unit.Key = 1056, "S01E1056"
	for _, name := range []string{"狂飙.2023.S01E1056.mkv", "The.Knockout.2023.S01E1056.{tmdb-100}.mkv"} {
		if got, err := media.ValidateTVEpisodeUpgradeTorrentFiles([]media.TVTorrentFile{{Path: name, Size: 1}}, target); err != nil || got == nil {
			t.Fatal("canonical original alias rejected", err)
		}
	}
}

func TestTVEpisodeUpgradeTorrentFilesRejectsAmbiguity(t *testing.T) {
	video := media.TVTorrentFile{Path: "Fixture.2020.S01E03.mkv", Size: 7}
	for _, name := range []string{
		"Other.2020.S01E03.mkv", "Fixture.2021.S01E03.mkv", "Fixture.2020.S01E04.mkv", "Fixture.S01E03.mkv", "S01E03.mkv",
		"Fixture.2020.S01E03.{tmdb-999}.mkv", "Fixture.2020.S01E03.tt1234567.mkv", "Fixture.2020.1x03.mkv",
		"Fixture.2020.S01E03E04.mkv", "Fixture.2020.S01E03-E04.mkv", "Fixture.2020.S01.COMPLETE.mkv", "Fixture.2020.2020-01-03.mkv", "Fixture.2020.E03.mkv", "[Team] Fixture.2020 - 03 [1080p].mkv",
		"Fixture.2020.S01E03.part1.mkv", "Fixture.2020.S01E03.sample.mkv", "Fixture.2020.S01E03.rar",
		"../Fixture.2020.S01E03.mkv", "Other/Fixture.2020.S01E03.mkv", "Fixture.2020.S02/Fixture.2020.S01E03.mkv", "Fixture.2020.S01E04/Fixture.2020.S01E03.mkv", "Fixture.2020.S01E03/S01E03.mkv",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := media.ValidateTVEpisodeUpgradeTorrentFiles([]media.TVTorrentFile{{Path: name, Size: 7}}, tvEpisodeUpgradeTarget())
			if got != nil || err != media.ErrTVEpisodeUpgradeTorrentFilesReview {
				t.Fatalf("accepted %q: %#v, %v", name, got, err)
			}
		})
	}
	for _, sidecar := range []string{"Other.2020.S01E03.srt", "Fixture.2020.S01E04.srt", "Fixture.2020.S01E03E04.nfo", "Fixture.2020.S01.COMPLETE.jpg", "Fixture.2020.S01E03.unknown.srt", "Fixture.2020.S01E03.en.Other.srt", "Fixture.2020.S01E03\u200b.srt", "extra.srt", "poster\u200b.jpg", "../readme.txt", "Other/poster.jpg"} {
		t.Run(sidecar, func(t *testing.T) {
			got, err := media.ValidateTVEpisodeUpgradeTorrentFiles([]media.TVTorrentFile{video, {Path: sidecar}}, tvEpisodeUpgradeTarget())
			if got != nil || err != media.ErrTVEpisodeUpgradeTorrentFilesReview {
				t.Fatalf("ambiguous sidecar accepted: %q", sidecar)
			}
		})
	}
	for _, files := range [][]media.TVTorrentFile{nil, {{Path: "poster.jpg", Size: 1}}, {{Path: video.Path}}, {{Path: video.Path, Size: -1}}, {video, video}, {video, {Path: "Fixture.2020.S01E03.mp4", Size: 1}}, {video, {Path: "sample.mkv", Size: 1}}, {video, {Path: "POSTER.JPG"}, {Path: "poster.jpg"}}, {video, {Path: "readme.txt", Size: -1}}, {{Path: video.Path, Size: math.MaxInt64}, {Path: "readme.txt", Size: 1}}} {
		if got, err := media.ValidateTVEpisodeUpgradeTorrentFiles(files, tvEpisodeUpgradeTarget()); got != nil || err != media.ErrTVEpisodeUpgradeTorrentFilesReview {
			t.Fatal("invalid file set accepted", got, err)
		}
	}
}

func TestTVEpisodeUpgradeTorrentFilesInvalidCanonicalTarget(t *testing.T) {
	for _, mutate := range []func(*media.TVEpisodeUpgradeTarget){
		func(v *media.TVEpisodeUpgradeTarget) { v.Unit.ID = uuid.Nil },
		func(v *media.TVEpisodeUpgradeTarget) { v.Unit.Version = 0 },
		func(v *media.TVEpisodeUpgradeTarget) { v.Unit.EpisodeID = 0 },
		func(v *media.TVEpisodeUpgradeTarget) { v.Unit.Retired = true },
		func(v *media.TVEpisodeUpgradeTarget) { v.Unit.Key = "S01E04" },
		func(v *media.TVEpisodeUpgradeTarget) { v.Unit.Season = 0; v.Unit.Key = "S00E03" },
		func(v *media.TVEpisodeUpgradeTarget) { v.TMDBID = 0 },
		func(v *media.TVEpisodeUpgradeTarget) { v.Year = 0 },
		func(v *media.TVEpisodeUpgradeTarget) { v.Title = "" },
	} {
		target := tvEpisodeUpgradeTarget()
		mutate(&target)
		if got, err := media.ValidateTVEpisodeUpgradeTorrentFiles([]media.TVTorrentFile{{Path: "Fixture.2020.S01E03.mkv", Size: 7}}, target); got != nil || err != media.ErrTVEpisodeUpgradeTorrentFilesReview {
			t.Fatal("invalid target accepted", got, err)
		}
	}
}
