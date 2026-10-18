package torrentname

import (
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   0.9,
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
				Confidence:   1.0,
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
				Audio:        "TRUEHD",
				ReleaseGroup: "COASTER",
				Confidence:   1.0,
			},
		},
		{
			name:  "web release with container",
			input: "The.Mandalorian.S02E08.1080p.WEBRip.x265-RARBG.mkv",
			expected: &TorrentInfo{
				Title:        "The Mandalorian",
				Season:       2,
				Episodes:     []int{8},
				Resolution:   "1080p",
				Source:       "WEBRip",
				Codec:        "H265",
				Container:    "mkv",
				ReleaseGroup: "RARBG",
				Confidence:   0.95,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
			},
		},
		{
			name:  "alternative episode format",
			input: "House.1x01.Pilot.720p.HDTV.x264",
			expected: &TorrentInfo{
				Title:      "House",
				Season:     1,
				Episodes:   []int{1},
				Resolution: "720p",
				Source:     "HDTV",
				Codec:      "H264",
				Confidence: 0.8,
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
				Confidence:   0.9,
			},
		},
		{
			name:  "repack release",
			input: "The.Witcher.S01E01.REPACK.1080p.WEB.H264-METCON",
			expected: &TorrentInfo{
				Title:        "The Witcher",
				Season:       1,
				Episodes:     []int{1},
				IsRepack:     true,
				Resolution:   "1080p",
				Source:       "WEBRip",
				Codec:        "H264",
				ReleaseGroup: "METCON",
				Confidence:   0.95,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   0.8,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   0.8,
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
				Confidence:   0.8,
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
				Confidence:   0.8,
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
				Confidence:   0.8,
			},
		},
		{
			name:  "hardcoded subtitles",
			input: "Squid.Game.S01E01.HC.1080p.WEBRip",
			expected: &TorrentInfo{
				Title:       "Squid Game",
				Season:      1,
				Episodes:    []int{1},
				IsHardcoded: true,
				Resolution:  "1080p",
				Source:      "WEBRip",
				Confidence:  0.75,
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
				Confidence: 0.8,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence: 0.95,
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
				Confidence: 0.8,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
			},
		},
		{
			name:  "minimal info low confidence",
			input: "Some Movie",
			expected: &TorrentInfo{
				Title:      "Some Movie",
				Confidence: 0.4,
			},
		},
		{
			name:  "medium info medium confidence",
			input: "Avatar.2009.1080p",
			expected: &TorrentInfo{
				Title:      "Avatar",
				Year:       2009,
				Resolution: "1080p",
				Confidence: 0.7,
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
				Confidence:   0.9,
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
				Confidence:   1.0,
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
				Confidence:   1.0,
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
				Confidence:   0.85,
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
				Confidence:   0.9,
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
				Confidence:   1.0,
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
				Confidence:   0.85,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.input)

			// Compare major fields
			if result.Title != tt.expected.Title {
				t.Errorf("Title: got %q, want %q", result.Title, tt.expected.Title)
			}
			if result.Year != tt.expected.Year {
				t.Errorf("Year: got %d, want %d", result.Year, tt.expected.Year)
			}
			if result.Date != tt.expected.Date {
				t.Errorf("Date: got %q, want %q", result.Date, tt.expected.Date)
			}
			if result.Season != tt.expected.Season {
				t.Errorf("Season: got %d, want %d", result.Season, tt.expected.Season)
			}
			if !intSlicesEqual(result.Episodes, tt.expected.Episodes) {
				t.Errorf("Episodes: got %v, want %v", result.Episodes, tt.expected.Episodes)
			}
			if result.Resolution != tt.expected.Resolution {
				t.Errorf("Resolution: got %q, want %q", result.Resolution, tt.expected.Resolution)
			}
			if result.Source != tt.expected.Source {
				t.Errorf("Source: got %q, want %q", result.Source, tt.expected.Source)
			}
			if result.Codec != tt.expected.Codec {
				t.Errorf("Codec: got %q, want %q", result.Codec, tt.expected.Codec)
			}
			if result.Audio != tt.expected.Audio {
				t.Errorf("Audio: got %q, want %q", result.Audio, tt.expected.Audio)
			}
			if result.ReleaseGroup != tt.expected.ReleaseGroup {
				t.Errorf("ReleaseGroup: got %q, want %q", result.ReleaseGroup, tt.expected.ReleaseGroup)
			}
			if result.Container != tt.expected.Container {
				t.Errorf("Container: got %q, want %q", result.Container, tt.expected.Container)
			}
			if result.Language != tt.expected.Language {
				t.Errorf("Language: got %q, want %q", result.Language, tt.expected.Language)
			}
			if result.IsComplete != tt.expected.IsComplete {
				t.Errorf("IsComplete: got %v, want %v", result.IsComplete, tt.expected.IsComplete)
			}
			if result.IsProper != tt.expected.IsProper {
				t.Errorf("IsProper: got %v, want %v", result.IsProper, tt.expected.IsProper)
			}
			if result.IsRepack != tt.expected.IsRepack {
				t.Errorf("IsRepack: got %v, want %v", result.IsRepack, tt.expected.IsRepack)
			}
			if result.IsHardcoded != tt.expected.IsHardcoded {
				t.Errorf("IsHardcoded: got %v, want %v", result.IsHardcoded, tt.expected.IsHardcoded)
			}
			if result.Edition != tt.expected.Edition {
				t.Errorf("Edition: got %q, want %q", result.Edition, tt.expected.Edition)
			}
			if result.Confidence != tt.expected.Confidence {
				t.Errorf("Confidence: got %f, want %f", result.Confidence, tt.expected.Confidence)
			}
		})
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

func intSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
