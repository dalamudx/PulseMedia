package media

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func torrentCoverageFixture() ([]TVTorrentFile, []NumberingRow, []NumberingUnit) {
	a := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	b := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	return []TVTorrentFile{{"Show/Show.S01E02.mkv", 4}, {"Show/Show.S01E01.mkv", 4}, {"Show/Show.S01E01.srt", 1}}, []NumberingRow{{1, 101, a, "S01E01", 3}, {2, 102, b, "S01E02", 4}}, []NumberingUnit{{a, 101, 1, 1, "S01E01", 3, false}, {b, 102, 1, 2, "S01E02", 4, false}}
}

func TestCompleteTVTorrentFilesExactIdentityAndOrder(t *testing.T) {
	files, rows, units := torrentCoverageFixture()
	before := slices.Clone(files)
	got, err := ValidateCompleteTVTorrentFiles(files, rows, units)
	if err != nil || len(got) != 2 || got[0].Unit != rows[0] || got[1].Unit != rows[1] || got[0].Path != "Show/Show.S01E01.mkv" || !slices.Equal(before, files) {
		t.Fatal(got, err)
	}
	files[0].Path = "Show.S01E01E02/Show.S01E02.mkv"
	if got, err = ValidateCompleteTVTorrentFiles(files, rows, units); err != nil || len(got) != 2 {
		t.Fatal("matching parent scope rejected", err)
	}
	// Provider metadata need not be contiguous. Never fill an unasserted middle unit.
	rows[1].SourceNumber = 3
	rows[1].UnitKey = "S01E03"
	units[1].Episode = 3
	units[1].Key = "S01E03"
	files[0].Path = "Show/Show.S01E03.mkv"
	got, err = ValidateCompleteTVTorrentFiles(files, rows, units)
	if err != nil || len(got) != 2 || got[1].Unit.UnitKey != "S01E03" {
		t.Fatal("noncontiguous explicit set changed", got, err)
	}
}

func TestCompleteTVTorrentFilesRefuseCoverageAndIdentityChanges(t *testing.T) {
	cases := map[string]func(*[]TVTorrentFile, *[]NumberingRow, *[]NumberingUnit){
		"missing_video": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { *f = (*f)[1:] },
		"extra_video": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			*f = append(*f, TVTorrentFile{"Show/Show.S01E03.mkv", 4})
		},
		"duplicate_episode": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Show/Other.S01E01.mp4"
		},
		"multiunit_file": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Show/Show.S01E01E02.mkv"
		},
		"absolute_without_mapping": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { (*f)[0].Path = "Show/Show - 02.mkv" },
		"unknown_video":            func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { (*f)[0].Path = "Show/02.mkv" },
		"movie_id_cannot_replace_episode": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Show/Show.2025.tmdb-123.mkv"
		},
		"other_season": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { (*f)[0].Path = "Show/Show.S02E02.mkv" },
		"special":      func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { (*f)[0].Path = "Show/Show.S00E02.mkv" },
		"sample": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Show/Show.S01E02.sample.mkv"
		},
		"archive": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			*f = append(*f, TVTorrentFile{"Show/Show.part01.rar", 4})
		},
		"unknown_attachment": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			*f = append(*f, TVTorrentFile{"Show/installer.exe", 4})
		},
		"empty_video":    func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { (*f)[0].Size = 0 },
		"negative_size":  func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { (*f)[0].Size = -1 },
		"duplicate_path": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { *f = append(*f, (*f)[0]) },
		"path_escape":    func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { (*f)[0].Path = "../Show.S01E02.mkv" },
		"backslash_path": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Show\\Show.S01E02.mkv"
		},
		"stale_version":    func(_ *[]TVTorrentFile, _ *[]NumberingRow, u *[]NumberingUnit) { (*u)[0].Version++ },
		"changed_provider": func(_ *[]TVTorrentFile, _ *[]NumberingRow, u *[]NumberingUnit) { (*u)[0].EpisodeID++ },
		"changed_uuid":     func(_ *[]TVTorrentFile, _ *[]NumberingRow, u *[]NumberingUnit) { (*u)[0].ID = uuid.New() },
		"retired":          func(_ *[]TVTorrentFile, _ *[]NumberingRow, u *[]NumberingUnit) { (*u)[0].Retired = true },
		"extra_canonical": func(_ *[]TVTorrentFile, _ *[]NumberingRow, u *[]NumberingUnit) {
			*u = append(*u, NumberingUnit{uuid.New(), 103, 1, 3, "S01E03", 1, false})
		},
		"hidden_missing_season": func(_ *[]TVTorrentFile, _ *[]NumberingRow, u *[]NumberingUnit) {
			*u = append(*u, NumberingUnit{uuid.New(), 103, 0, 3, "S01E03", 1, false})
		},
		"duplicate_approval":        func(_ *[]TVTorrentFile, r *[]NumberingRow, _ *[]NumberingUnit) { (*r)[1] = (*r)[0] },
		"conflicting_parent_season": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) { (*f)[0].Path = "S02/Show.S01E02.mkv" },
		"conflicting_parent_episode": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "S01E01/Show.S01E02.mkv"
		},
		"partial_parent": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Sample/Show.S01E02.mkv"
		},
		"file_directory_alias": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			*f = append(*f, TVTorrentFile{"Show/Show.S01E02.mkv/readme.txt", 1})
		},
		"parent_coverage_expansion": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Show.S01E01E02E03/Show.S01E02.mkv"
		},
		"parent_all_seasons": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Show All Seasons (1-8)/Show.S01E02.mkv"
		},
		"parent_collection": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Show 3 Movie Collection/Show.S01E02.mkv"
		},
		"parent_archive": func(f *[]TVTorrentFile, _ *[]NumberingRow, _ *[]NumberingUnit) {
			(*f)[0].Path = "Show.2025.part01.rar/Show.S01E02.mkv"
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f, r, u := torrentCoverageFixture()
			change(&f, &r, &u)
			got, err := ValidateCompleteTVTorrentFiles(f, r, u)
			if got != nil || !errors.Is(err, ErrTVCoverageFilesRequireReview) {
				t.Fatal("scope/identity accepted", got, err)
			}
		})
	}
}
