package torrentname

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected *TorrentInfo
	}{
		{
			name:  "movie with year and quality",
			input: "The.Matrix.1999.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Matrix",
				Year:         1999,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with extended edition",
			input: "The.Lord.of.the.Rings.The.Fellowship.of.the.Ring.2001.EXTENDED.1080p.BluRay.x265-RARBG",
			expected: &TorrentInfo{
				Title:        "The Lord of the Rings The Fellowship of the Ring",
				Year:         2001,
				Edition:      "Extended",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H265",
				ReleaseGroup: "RARBG",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "complete_season_pack",
			input: "Game.of.Thrones.S08.Complete.1080p.BluRay.x264-ROVERS[rartv]",
			expected: &TorrentInfo{
				Title:        "Game of Thrones",
				Season:       8,
				IsComplete:   true,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "ROVERS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with proper",
			input: "Inception.2010.1080p.BluRay.x264.PROPER-SPARKS",
			expected: &TorrentInfo{
				Title:        "Inception",
				Year:         2010,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				IsProper:     true,
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "4k hdr content",
			input: "Blade.Runner.2049.2017.2160p.BluRay.HEVC.TrueHD.7.1.Atmos-COASTER",
			expected: &TorrentInfo{
				Title:        "Blade Runner 2049",
				Year:         2017,
				Resolution:   "2160p",
				Source:       "BluRay",
				Codec:        "H265",
				Audio:        "TRUEHD 7.1 ATMOS",
				ReleaseGroup: "COASTER",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "web release with container",
			input: "The.Mandalorian.S02E08.1080p.WEBRip.x265-RARBG.mkv",
			expected: &TorrentInfo{
				Title:        "The Mandalorian",
				Season:       2,
				Episode:      8,
				Resolution:   "1080p",
				Source:       "WEBRip",
				Codec:        "H265",
				Container:    "mkv",
				ReleaseGroup: "RARBG",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "directors cut",
			input: "Aliens.1986.Directors.Cut.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "Aliens",
				Year:         1986,
				Edition:      "Directors Cut",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "foreign movie with subtitles",
			input: "Parasite.2019.KOREAN.1080p.BluRay.x264.DTS-FGT",
			expected: &TorrentInfo{
				Title:        "Parasite",
				Year:         2019,
				Language:     "Korean",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				Audio:        "DTS",
				ReleaseGroup: "FGT",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "alternative episode format",
			input: "House.1x01.Pilot.720p.HDTV.x264",
			expected: &TorrentInfo{
				Title:      "House",
				Season:     1,
				Episode:    1,
				Resolution: "720p",
				Source:     "HDTV",
				Codec:      "H264",
				Unparsed:   "Pilot",
				Confidence: YearSeasonWeight + ResolutionWeight + SourceWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "cam release",
			input: "Avengers.Endgame.2019.CAM.x264-ETRG",
			expected: &TorrentInfo{
				Title:        "Avengers Endgame",
				Year:         2019,
				Source:       "CAM",
				Codec:        "H264",
				ReleaseGroup: "ETRG",
				Confidence:   YearSeasonWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "repack release",
			input: "The.Witcher.S01E01.REPACK.1080p.WEB.H264-METCON",
			expected: &TorrentInfo{
				Title:        "The Witcher",
				Season:       1,
				Episode:      1,
				IsRepack:     true,
				Resolution:   "1080p",
				Source:       "WEBRip",
				Codec:        "H264",
				ReleaseGroup: "METCON",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with year in title",
			input: "2001.A.Space.Odyssey.1968.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "2001 A Space Odyssey",
				Year:         1968,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie titled 1941 released in 1979",
			input: "1941.1979.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "1941",
				Year:         1979,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie titled 1984 with no release year",
			input: "1984.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "1984",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "title not containing year, release year after metadata start",
			input: "The.Matrix.1080p.BluRay.1999.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Matrix",
				Year:         1999,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "title containing year, release year after metadata start",
			input: "2001.A.Space.Odyssey.1080p.BluRay.1968.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "2001 A Space Odyssey",
				Year:         1968,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "title not containing year, release year immediately after title",
			input: "The.Matrix.1999.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Matrix",
				Year:         1999,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "title containing but not ending in year, release year immediately after title",
			input: "2001.A.Space.Odyssey.1968.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "2001 A Space Odyssey",
				Year:         1968,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "title not containing year, no release year",
			input: "Some.Movie.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "Some Movie",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "title containing but not ending in year, no release year",
			input: "The.Year.2000.Problem.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Year 2000 Problem",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "title ending in non-release year (before 1895), no release year",
			input: "The.Year.1800.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Year 1800",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "title ending in non-release year (far future), no release year",
			input: "The.Year.3000.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Year 3000",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "hardcoded subtitles",
			input: "Squid.Game.S01E01.HC.1080p.WEBRip",
			expected: &TorrentInfo{
				Title:       "Squid Game",
				Season:      1,
				Episode:     1,
				IsHardcoded: true,
				Resolution:  "1080p",
				Source:      "WEBRip",
				Confidence:  YearSeasonWeight + ResolutionWeight + SourceWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "daily show format",
			input: "The.Daily.Show.2023.10.15.1080p.WEB",
			expected: &TorrentInfo{
				Title:      "The Daily Show",
				Year:       2023,
				Date:       "2023.10.15",
				Resolution: "1080p",
				Source:     "WEBRip",
				Confidence: YearSeasonWeight + ResolutionWeight + SourceWeight,
			},
		},
		{
			name:  "no dots separator",
			input: "The Matrix 1999 1080p BluRay x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Matrix",
				Year:         1999,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "mixed case",
			input: "tHe.MaTrIx.1999.1080P.bLuRaY.X264-SPARKS",
			expected: &TorrentInfo{
				Title:        "tHe MaTrIx",
				Year:         1999,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "mp4 container",
			input: "Avengers.Endgame.2019.1080p.BluRay.x264-SPARKS.mp4",
			expected: &TorrentInfo{
				Title:        "Avengers Endgame",
				Year:         2019,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				Container:    "mp4",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "avi container",
			input: "Some.Movie.2000.720p.HDTV.x264.avi",
			expected: &TorrentInfo{
				Title:      "Some Movie",
				Year:       2000,
				Resolution: "720p",
				Source:     "HDTV",
				Codec:      "H264",
				Container:  "avi",
				Confidence: YearSeasonWeight + ResolutionWeight + SourceWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 'Show' in title",
			input: "The.Daily.Show.2023.10.15.1080p.WEB",
			expected: &TorrentInfo{
				Title:      "The Daily Show",
				Year:       2023,
				Date:       "2023.10.15",
				Resolution: "1080p",
				Source:     "WEBRip",
				Confidence: YearSeasonWeight + ResolutionWeight + SourceWeight,
			},
		},
		{
			name:  "movie with 'Extended' in title",
			input: "The.Lord.of.the.Rings.The.Fellowship.of.the.Ring.2001.EXTENDED.1080p.BluRay.x265-RARBG",
			expected: &TorrentInfo{
				Title:        "The Lord of the Rings The Fellowship of the Ring",
				Year:         2001,
				Edition:      "Extended",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H265",
				ReleaseGroup: "RARBG",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 'Complete' in title",
			input: "The.Complete.Works.of.Shakespeare.1995.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Complete Works of Shakespeare",
				Year:         1995,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 'Proper' in title",
			input: "The.Proper.Way.to.Cook.2010.720p.HDTV.x264-ROVERS",
			expected: &TorrentInfo{
				Title:        "The Proper Way to Cook",
				Year:         2010,
				Resolution:   "720p",
				Source:       "HDTV",
				Codec:        "H264",
				ReleaseGroup: "ROVERS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 'Repack' in title",
			input: "How.to.Repack.Your.Bags.2015.1080p.WEB-DL.x264-FGT",
			expected: &TorrentInfo{
				Title:        "How to Repack Your Bags",
				Year:         2015,
				Resolution:   "1080p",
				Source:       "WEB-DL",
				Codec:        "H264",
				ReleaseGroup: "FGT",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 'Unrated' in title",
			input: "The.Unrated.Story.2008.720p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Unrated Story",
				Year:         2008,
				Resolution:   "720p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 'Theatrical' in title",
			input: "The.Theatrical.Experience.2012.1080p.WEBRip.x265-RARBG",
			expected: &TorrentInfo{
				Title:        "The Theatrical Experience",
				Year:         2012,
				Resolution:   "1080p",
				Source:       "WEBRip",
				Codec:        "H265",
				ReleaseGroup: "RARBG",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 'Directors' in title",
			input: "The.Directors.Guild.2019.720p.HDTV.x264-ETRG",
			expected: &TorrentInfo{
				Title:        "The Directors Guild",
				Year:         2019,
				Resolution:   "720p",
				Source:       "HDTV",
				Codec:        "H264",
				ReleaseGroup: "ETRG",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 'Cut' in title",
			input: "The.Cut.Throat.Business.2017.1080p.BluRay.x264-COASTER",
			expected: &TorrentInfo{
				Title:        "The Cut Throat Business",
				Year:         2017,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "COASTER",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 'Final' in title",
			input: "The.Final.Countdown.1980.1080p.BluRay.x264-SPARKS",
			expected: &TorrentInfo{
				Title:        "The Final Countdown",
				Year:         1980,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "minimal info low confidence",
			input: "Some Movie",
			expected: &TorrentInfo{
				Title:      "Some Movie",
				Confidence: 0,
			},
		},
		{
			name:  "medium info medium confidence",
			input: "Avatar.2009.1080p",
			expected: &TorrentInfo{
				Title:      "Avatar",
				Year:       2009,
				Resolution: "1080p",
				Confidence: YearSeasonWeight + ResolutionWeight,
			},
		},
		{
			name:  "BTN season pack format",
			input: "Breaking.Bad.S01.Complete.720p.BluRay.x264-DEMAND",
			expected: &TorrentInfo{
				Title:        "Breaking Bad",
				Season:       1,
				IsComplete:   true,
				Resolution:   "720p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "DEMAND",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "HDBits format",
			input: "The.Dark.Knight.2008.1080p.BluRay.DTS.x264-ESiR",
			expected: &TorrentInfo{
				Title:        "The Dark Knight",
				Year:         2008,
				Resolution:   "1080p",
				Source:       "BluRay",
				Audio:        "DTS",
				Codec:        "H264",
				ReleaseGroup: "ESiR",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "year before and after metadata start",
			input: "Movie.1995.1080p.2010.BluRay.x264-GROUP",
			expected: &TorrentInfo{
				Title:        "Movie 1995",
				Year:         2010,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "GROUP",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "edition before and after metadata start",
			input: "Epic.Film.Extended.1080p.THEATRICAL.BluRay.x264-GROUP",
			expected: &TorrentInfo{
				Title:        "Epic Film Extended",
				Edition:      "Theatrical",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "GROUP",
				Confidence:   ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "keyword before and after metadata start",
			input: "Show.Complete.720p.PROPER.HDTV.x264-GROUP",
			expected: &TorrentInfo{
				Title:        "Show",
				IsComplete:   true,
				IsProper:     true,
				Resolution:   "720p",
				Source:       "HDTV",
				Codec:        "H264",
				ReleaseGroup: "GROUP",
				Confidence:   ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "title contains year, real year after metadata start",
			input: "The.Year.2000.Problem.1080p.2001.BluRay.x264-GROUP",
			expected: &TorrentInfo{
				Title:        "The Year 2000 Problem",
				Year:         2001,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "GROUP",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight,
			},
		},
		{
			name:  "multi-word_edition_in_title,_real_edition_after_metadata_start",
			input: "Aliens.Directors.Cut.1080p.FINAL.CUT.BluRay.x264-GROUP",
			expected: &TorrentInfo{
				Title:        "Aliens Directors Cut",
				Edition:      "Final Cut",
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "GROUP",
				Confidence:   ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "tv episode with unparsed title",
			input: "Breaking Bad S01E01 Pilot 1080p",
			expected: &TorrentInfo{
				Title:      "Breaking Bad",
				Season:     1,
				Episode:    1,
				Resolution: "1080p",
				Unparsed:   "Pilot",
				Confidence: YearSeasonWeight + ResolutionWeight + MinorFieldWeight,
			},
		},
		{
			name:  "movie with 2.0 in title and audio metadata",
			input: "Godzilla 2.0 1080p TrueHD 7.1 Atmos",
			expected: &TorrentInfo{
				Title:      "Godzilla 2 0",
				Resolution: "1080p",
				Audio:      "TRUEHD 7.1 ATMOS",
				Confidence: ResolutionWeight + MinorFieldWeight,
			},
		},
		{
			name:  "mono audio after metadata boundary",
			input: "Classic.Movie.1950.480p.DVD.Mono.x264-GROUP",
			expected: &TorrentInfo{
				Title:        "Classic Movie",
				Year:         1950,
				Resolution:   "480p",
				Source:       "DVD",
				Audio:        "MONO",
				Codec:        "H264",
				ReleaseGroup: "GROUP",
				Confidence:   YearSeasonWeight + ResolutionWeight + SourceWeight + ReleaseGroupWeight + MinorFieldWeight + MinorFieldWeight,
			},
		},
		{
			name:  "duplicate definitely metadata",
			input: "Some.Movie.2020.1080p.720p.BluRay.WEB.x264.H265-GROUP",
			expected: &TorrentInfo{
				Title:        "Some Movie 2020 1080p 720p BluRay WEB x264",
				Codec:        "H265", // First codec found (back-to-front scan)
				ReleaseGroup: "GROUP",
				Confidence:   ReleaseGroupWeight + MinorFieldWeight,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.input)

			// Full struct comparison
			compareTorrentInfo(t, result, tt.expected)
		})
	}
}

// compareTorrentInfo checks all fields for equality, including omitempty and slices
func compareTorrentInfo(t *testing.T, got, want *TorrentInfo) {
	if got == nil && want == nil {
		return
	}
	if got == nil || want == nil {
		t.Errorf("TorrentInfo: got %v, want %v", got, want)
		return
	}
	if got.Title != want.Title {
		t.Errorf("Title: got %q, want %q", got.Title, want.Title)
	}
	if got.Year != want.Year {
		t.Errorf("Year: got %d, want %d", got.Year, want.Year)
	}
	if got.Date != want.Date {
		t.Errorf("Date: got %q, want %q", got.Date, want.Date)
	}
	if got.Season != want.Season {
		t.Errorf("Season: got %d, want %d", got.Season, want.Season)
	}
	if got.Episode != want.Episode {
		t.Errorf("Episode: got %d, want %d", got.Episode, want.Episode)
	}
	if got.Resolution != want.Resolution {
		t.Errorf("Resolution: got %q, want %q", got.Resolution, want.Resolution)
	}
	if got.Source != want.Source {
		t.Errorf("Source: got %q, want %q", got.Source, want.Source)
	}
	if got.Codec != want.Codec {
		t.Errorf("Codec: got %q, want %q", got.Codec, want.Codec)
	}
	if got.Audio != want.Audio {
		t.Errorf("Audio: got %q, want %q", got.Audio, want.Audio)
	}
	if got.ReleaseGroup != want.ReleaseGroup {
		t.Errorf("ReleaseGroup: got %q, want %q", got.ReleaseGroup, want.ReleaseGroup)
	}
	if got.Container != want.Container {
		t.Errorf("Container: got %q, want %q", got.Container, want.Container)
	}
	if got.Language != want.Language {
		t.Errorf("Language: got %q, want %q", got.Language, want.Language)
	}
	if !reflect.DeepEqual(got.Subtitles, want.Subtitles) {
		t.Errorf("Subtitles: got %v, want %v", got.Subtitles, want.Subtitles)
	}
	if got.IsComplete != want.IsComplete {
		t.Errorf("IsComplete: got %v, want %v", got.IsComplete, want.IsComplete)
	}
	if got.IsProper != want.IsProper {
		t.Errorf("IsProper: got %v, want %v", got.IsProper, want.IsProper)
	}
	if got.IsRepack != want.IsRepack {
		t.Errorf("IsRepack: got %v, want %v", got.IsRepack, want.IsRepack)
	}
	if got.IsHardcoded != want.IsHardcoded {
		t.Errorf("IsHardcoded: got %v, want %v", got.IsHardcoded, want.IsHardcoded)
	}
	if got.Edition != want.Edition {
		t.Errorf("Edition: got %q, want %q", got.Edition, want.Edition)
	}
	if got.Confidence != want.Confidence {
		t.Errorf("Confidence: got %d, want %d", got.Confidence, want.Confidence)
	}
	if got.Unparsed != want.Unparsed {
		t.Errorf("Unparsed: got %q, want %q", got.Unparsed, want.Unparsed)
	}
}

func BenchmarkParse(b *testing.B) {
	torrentNames := []string{
		"The.Matrix.1999.1080p.BluRay.x264-SPARKS",
		"Breaking.Bad.S01E01.Pilot.1080p.BluRay.x264-ROVERS",
		"Game.of.Thrones.S08.COMPLETE.1080p.BluRay.x264-ROVERS[rartv]",
		"The.Lord.of.the.Rings.The.Fellowship.of.the.Ring.2001.EXTENDED.1080p.BluRay.x265-RARBG",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, name := range torrentNames {
			Parse(name)
		}
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple title",
			input:    "The Matrix",
			expected: "matrix",
		},
		{
			name:     "title with special characters",
			input:    "The.Matrix.1999.1080p.BluRay.x264-SPARKS",
			expected: "matrix 1999 1080p bluray x264 sparks",
		},
		{
			name:     "title with brackets",
			input:    "The Matrix [1999] (Extended)",
			expected: "matrix 1999 extended",
		},
		{
			name:     "title with underscores",
			input:    "The_Matrix_1999",
			expected: "matrix 1999",
		},
		{
			name:     "title with numbers",
			input:    "2001 A Space Odyssey",
			expected: "2001 space odyssey",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "only common words",
			input:    "The A And Of",
			expected: "",
		},
		{
			name:     "mixed case",
			input:    "The MATRIX and the Reloaded",
			expected: "matrix reloaded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeTitle(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeTitle(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMatchTitles(t *testing.T) {
	tests := []struct {
		name      string
		title1    string
		title2    string
		threshold float64
		expected  bool
	}{
		{
			name:      "exact match after normalization",
			title1:    "The Matrix",
			title2:    "The Matrix",
			threshold: TitleMatchThreshold,
			expected:  true,
		},
		{
			name:      "similar titles with high threshold",
			title1:    "The Matrix",
			title2:    "Matrix",
			threshold: 0.9,
			expected:  true,
		},
		{
			name:      "similar titles with low threshold",
			title1:    "The Matrix",
			title2:    "Matrix",
			threshold: 0.3,
			expected:  true,
		},
		{
			name:      "different titles with high threshold",
			title1:    "The Matrix",
			title2:    "The Terminator",
			threshold: 0.9,
			expected:  false,
		},
		{
			name:      "different titles with low threshold",
			title1:    "The Matrix",
			title2:    "The Terminator",
			threshold: 0.3,
			expected:  false,
		},
		{
			name:      "titles with special characters",
			title1:    "The.Matrix.",
			title2:    "The Matrix",
			threshold: TitleMatchThreshold,
			expected:  true,
		},
		{
			name:      "titles with different formatting",
			title1:    "The Lord of the Rings",
			title2:    "Lord of the Rings",
			threshold: TitleMatchThreshold,
			expected:  true,
		},
		{
			name:      "completely different titles",
			title1:    "The Matrix",
			title2:    "Star Wars",
			threshold: TitleMatchThreshold,
			expected:  false,
		},
		{
			name:      "empty titles",
			title1:    "",
			title2:    "",
			threshold: TitleMatchThreshold,
			expected:  true,
		},
		{
			name:      "one empty title",
			title1:    "The Matrix",
			title2:    "",
			threshold: TitleMatchThreshold,
			expected:  false,
		},
		{
			name:      "threshold behavior - similar titles with high threshold",
			title1:    "Matrix",
			title2:    "Matrix Reloaded",
			threshold: TitleMatchThreshold,
			expected:  false,
		},
		{
			name:      "threshold behavior - similar titles with low threshold",
			title1:    "Matrix",
			title2:    "Matrix Reloaded",
			threshold: 0.3,
			expected:  true,
		},
		{
			name:      "threshold behavior - similar titles with medium threshold",
			title1:    "Matrix Reloaded",
			title2:    "Matrix Revolutions",
			threshold: 0.5,
			expected:  true,
		},
		{
			name:      "threshold behavior - similar titles with default threshold",
			title1:    "Matrix Reloaded",
			title2:    "Matrix Revolutions",
			threshold: TitleMatchThreshold,
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MatchTitles(tt.title1, tt.title2, tt.threshold)
			if result != tt.expected {
				t.Errorf("MatchTitles(%q, %q, %f) = %v, want %v", tt.title1, tt.title2, tt.threshold, result, tt.expected)
			}
		})
	}
}

func TestCalculateSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		s1       string
		s2       string
		expected float64
	}{
		{
			name:     "identical strings",
			s1:       "matrix",
			s2:       "matrix",
			expected: 1.0,
		},
		{
			name:     "completely different strings",
			s1:       "matrix",
			s2:       "terminator",
			expected: 0.0,
		},
		{
			name:     "partial overlap",
			s1:       "matrix reloaded",
			s2:       "matrix revolutions",
			expected: 0.5, // 1 common word out of 3 total unique words
		},
		{
			name:     "empty strings",
			s1:       "",
			s2:       "",
			expected: 0.0,
		},
		{
			name:     "one empty string",
			s1:       "matrix",
			s2:       "",
			expected: 0.0,
		},
		{
			name:     "same words different order",
			s1:       "matrix reloaded",
			s2:       "reloaded matrix",
			expected: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateSimilarity(tt.s1, tt.s2)
			if result != tt.expected {
				t.Errorf("calculateSimilarity(%q, %q) = %f, want %f", tt.s1, tt.s2, result, tt.expected)
			}
		})
	}
}
