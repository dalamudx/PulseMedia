package torrentname

import (
	"strings"
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
			name:  "tv show single episode",
			input: "Breaking.Bad.S01E01.Pilot.1080p.BluRay.x264-ROVERS",
			expected: &TorrentInfo{
				Title:        "Breaking Bad",
				Season:       1,
				Episodes:     []int{1},
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "ROVERS",
				Confidence:   1.0,
			},
		},
		{
			name:  "tv show multi-episode",
			input: "The.Walking.Dead.S05E01-E03.1080p.WEB-DL.DD5.1.H264-Cyphanix",
			expected: &TorrentInfo{
				Title:        "The Walking Dead",
				Season:       5,
				Episodes:     []int{1, 2, 3},
				Resolution:   "1080p",
				Source:       "WEB-DL",
				Audio:        "DD",
				Codec:        "H264",
				ReleaseGroup: "Cyphanix",
				Confidence:   1.0,
			},
		},
		{
			name:  "complete season pack",
			input: "Game.of.Thrones.S08.COMPLETE.1080p.BluRay.x264-ROVERS[rartv]",
			expected: &TorrentInfo{
				Title:        "Game of Thrones",
				Season:       8,
				IsComplete:   true,
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "ROVERS",
				Unparsed:     []string{"rartv"},
				Confidence:   1.0,
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
				Confidence:   1.0,
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
			name:  "episode only format",
			input: "The.Office.E01.Pilot.720p.HDTV.x264",
			expected: &TorrentInfo{
				Title:      "The Office",
				Season:     1,
				Episodes:   []int{1},
				Resolution: "720p",
				Source:     "HDTV",
				Codec:      "H264",
				Confidence: 0.8,
			},
		},
		{
			name:  "episode only with season context",
			input: "Breaking.Bad.S02.E05.720p.HDTV.x264",
			expected: &TorrentInfo{
				Title:      "Breaking Bad",
				Season:     2,
				Episodes:   []int{5},
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
				Confidence:   1.0,
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
				Confidence:   1.0,
			},
		},
		{
			name:  "minimal info",
			input: "Some.Random.Movie",
			expected: &TorrentInfo{
				Title:      "Some Random Movie",
				Confidence: 0.4,
			},
		},
		{
			name:  "bracketed release group",
			input: "The.Office.US.S09E23.1080p.BluRay.x265-RARBG[rartv]",
			expected: &TorrentInfo{
				Title:        "The Office US",
				Season:       9,
				Episodes:     []int{23},
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H265",
				ReleaseGroup: "RARBG",
				Unparsed:     []string{"rartv"},
				Confidence:   1.0,
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
				Year:         1984, // This is the limitation - we can't distinguish title year from release year
				Resolution:   "1080p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "SPARKS",
				Confidence:   1.0,
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
			if result.ReleaseGroup != tt.expected.ReleaseGroup {
				t.Errorf("ReleaseGroup: got %q, want %q", result.ReleaseGroup, tt.expected.ReleaseGroup)
			}
			if !slicesEqual(result.Unparsed, tt.expected.Unparsed) {
				t.Errorf("Unparsed: got %v, want %v", result.Unparsed, tt.expected.Unparsed)
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
			if result.Edition != tt.expected.Edition {
				t.Errorf("Edition: got %q, want %q", result.Edition, tt.expected.Edition)
			}
			if result.Language != tt.expected.Language {
				t.Errorf("Language: got %q, want %q", result.Language, tt.expected.Language)
			}
			if result.Audio != tt.expected.Audio {
				t.Errorf("Audio: got %q, want %q", result.Audio, tt.expected.Audio)
			}
			if result.Container != tt.expected.Container {
				t.Errorf("Container: got %q, want %q", result.Container, tt.expected.Container)
			}
			if result.Confidence < 0.9 {
				t.Errorf("Confidence too low for %s: got %f", tt.name, result.Confidence)
			}
		})
	}
}

func TestEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(*TorrentInfo) bool
	}{
		{
			name:  "multiple year patterns",
			input: "2001.A.Space.Odyssey.1968.1080p.BluRay",
			check: func(info *TorrentInfo) bool {
				return info.Year == 1968 && info.Title == "2001 A Space Odyssey"
			},
		},
		{
			name:  "episode range without dash",
			input: "The.Wire.S01E01E02E03.720p.HDTV",
			check: func(info *TorrentInfo) bool {
				return len(info.Episodes) >= 1 && info.Episodes[0] == 1
			},
		},
		{
			name:  "hardcoded subtitles",
			input: "Squid.Game.S01E01.HC.1080p.WEBRip",
			check: func(info *TorrentInfo) bool {
				return info.IsHardcoded == true
			},
		},
		{
			name:  "daily show format",
			input: "The.Daily.Show.2023.10.15.1080p.WEB",
			check: func(info *TorrentInfo) bool {
				return info.Title == "The Daily Show" && info.Year == 2023
			},
		},
		{
			name:  "no dots separator",
			input: "The Matrix 1999 1080p BluRay x264-SPARKS",
			check: func(info *TorrentInfo) bool {
				return info.Title == "The Matrix" && info.Year == 1999
			},
		},
		{
			name:  "mixed case",
			input: "tHe.MaTrIx.1999.1080P.bLuRaY.X264-SPARKS",
			check: func(info *TorrentInfo) bool {
				return info.Resolution == "1080p" && info.Source == "BluRay" && info.Title == "tHe MaTrIx"
			},
		},
		{
			name:  "episode only without season",
			input: "Friends.E10.The.One.With.Monica.720p.HDTV",
			check: func(info *TorrentInfo) bool {
				return info.Episodes[0] == 10 && info.Season == 1
			},
		},
		{
			name:  "episode only with episode in title",
			input: "Seinfeld.E15.The.Pilot.720p.HDTV",
			check: func(info *TorrentInfo) bool {
				return len(info.Episodes) > 0 && info.Episodes[0] == 15 && info.Season == 1
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.input)
			if !tt.check(result) {
				t.Errorf("Check failed for %q: %+v", tt.input, result)
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

func TestConfidenceCalculation(t *testing.T) {
	tests := []struct {
		name               string
		input              string
		expectedConfidence float64
		tolerance          float64
	}{
		{
			name:               "complete info high confidence",
			input:              "The.Matrix.1999.1080p.BluRay.x264-SPARKS",
			expectedConfidence: 1.0,
			tolerance:          0.01,
		},
		{
			name:               "minimal info low confidence",
			input:              "Some Movie",
			expectedConfidence: 0.4,
			tolerance:          0.1,
		},
		{
			name:               "medium info medium confidence",
			input:              "Avatar.2009.1080p",
			expectedConfidence: 0.8,
			tolerance:          0.1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.input)
			if diff := abs(result.Confidence - tt.expectedConfidence); diff > tt.tolerance {
				t.Errorf("Confidence: got %f, want %f (±%f)", result.Confidence, tt.expectedConfidence, tt.tolerance)
			}
		})
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func slicesEqual(a, b []string) bool {
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

func TestParseWithHints(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		tracker  string
		expected *TorrentInfo
	}{
		{
			name:    "BTN season pack format",
			input:   "Breaking.Bad.S01.Complete.720p.BluRay.x264-DEMAND",
			tracker: "BTN",
			expected: &TorrentInfo{
				Title:        "Breaking Bad",
				Season:       1,
				IsComplete:   true,
				Resolution:   "720p",
				Source:       "BluRay",
				Codec:        "H264",
				ReleaseGroup: "DEMAND",
				Confidence:   1.0,
			},
		},
		{
			name:    "HDBits format increases confidence",
			input:   "The.Dark.Knight.2008.1080p.BluRay.DTS.x264-ESiR",
			tracker: "HDBits",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseWithHints(tt.input, tt.tracker)

			if result.Title != tt.expected.Title {
				t.Errorf("Title: got %q, want %q", result.Title, tt.expected.Title)
			}
			if result.Season != tt.expected.Season {
				t.Errorf("Season: got %d, want %d", result.Season, tt.expected.Season)
			}
			if result.IsComplete != tt.expected.IsComplete {
				t.Errorf("IsComplete: got %v, want %v", result.IsComplete, tt.expected.IsComplete)
			}
			if result.Year != tt.expected.Year {
				t.Errorf("Year: got %d, want %d", result.Year, tt.expected.Year)
			}
			if result.Resolution != tt.expected.Resolution {
				t.Errorf("Resolution: got %q, want %q", result.Resolution, tt.expected.Resolution)
			}
			if result.Source != tt.expected.Source {
				t.Errorf("Source: got %q, want %q", result.Source, tt.expected.Source)
			}
			if result.Audio != tt.expected.Audio {
				t.Errorf("Audio: got %q, want %q", result.Audio, tt.expected.Audio)
			}
			if result.Codec != tt.expected.Codec {
				t.Errorf("Codec: got %q, want %q", result.Codec, tt.expected.Codec)
			}
			if result.ReleaseGroup != tt.expected.ReleaseGroup {
				t.Errorf("ReleaseGroup: got %q, want %q", result.ReleaseGroup, tt.expected.ReleaseGroup)
			}
			if result.Confidence < 0.9 {
				t.Errorf("Confidence too low for %s: got %f", tt.name, result.Confidence)
			}
		})
	}
}

func TestLanguageAndSubtitles(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected *TorrentInfo
	}{
		{
			name:  "foreign language with subtitles",
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
			name:  "with hardcoded subtitles",
			input: "Squid.Game.S01E01.HC.1080p.WEBRip.x265-RARBG",
			expected: &TorrentInfo{
				Title:        "Squid Game",
				Season:       1,
				Episodes:     []int{1},
				IsHardcoded:  true,
				Resolution:   "1080p",
				Source:       "WEBRip",
				Codec:        "H265",
				ReleaseGroup: "RARBG",
				Confidence:   1.0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.input)

			if result.Title != tt.expected.Title {
				t.Errorf("Title: got %q, want %q", result.Title, tt.expected.Title)
			}
			if result.Language != tt.expected.Language {
				t.Errorf("Language: got %q, want %q", result.Language, tt.expected.Language)
			}
			if result.IsHardcoded != tt.expected.IsHardcoded {
				t.Errorf("IsHardcoded: got %v, want %v", result.IsHardcoded, tt.expected.IsHardcoded)
			}
			if result.Confidence < 0.9 {
				t.Errorf("Confidence too low for %s: got %f", tt.name, result.Confidence)
			}
		})
	}
}

func TestContainerDetection(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "mkv container",
			input:    "The.Mandalorian.S02E08.1080p.WEBRip.x265-RARBG.mkv",
			expected: "mkv",
		},
		{
			name:     "mp4 container",
			input:    "Avengers.Endgame.2019.1080p.BluRay.x264-SPARKS.mp4",
			expected: "mp4",
		},
		{
			name:     "avi container",
			input:    "Some.Movie.2000.720p.HDTV.x264.avi",
			expected: "avi",
		},
		{
			name:     "no container",
			input:    "The.Matrix.1999.1080p.BluRay.x264-SPARKS",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.input)
			if result.Container != tt.expected {
				t.Errorf("Container: got %q, want %q", result.Container, tt.expected)
			}
		})
	}
}

func TestEpisodeOnlyPattern(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected *TorrentInfo
	}{
		{
			name:  "simple episode only",
			input: "The.Office.E01.Pilot.720p.HDTV.x264",
			expected: &TorrentInfo{
				Title:      "The Office",
				Season:     1,
				Episodes:   []int{1},
				Resolution: "720p",
				Source:     "HDTV",
				Codec:      "H264",
			},
		},
		{
			name:  "episode only with season context",
			input: "Breaking.Bad.S02.E05.720p.HDTV.x264",
			expected: &TorrentInfo{
				Title:      "Breaking Bad",
				Season:     2,
				Episodes:   []int{5},
				Resolution: "720p",
				Source:     "HDTV",
				Codec:      "H264",
			},
		},
		{
			name:  "episode only without season",
			input: "Friends.E10.The.One.With.Monica.720p.HDTV",
			expected: &TorrentInfo{
				Title:      "Friends",
				Season:     1,
				Episodes:   []int{10},
				Resolution: "720p",
				Source:     "HDTV",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.input)

			// Check that episodes are detected correctly
			if !intSlicesEqual(result.Episodes, tt.expected.Episodes) {
				t.Errorf("Episodes: got %v, want %v", result.Episodes, tt.expected.Episodes)
			}

			// Check that season is detected correctly
			if result.Season != tt.expected.Season {
				t.Errorf("Season: got %d, want %d", result.Season, tt.expected.Season)
			}

			// Check that resolution is detected
			if result.Resolution != tt.expected.Resolution {
				t.Errorf("Resolution: got %q, want %q", result.Resolution, tt.expected.Resolution)
			}

			// Check that source is detected
			if result.Source != tt.expected.Source {
				t.Errorf("Source: got %q, want %q", result.Source, tt.expected.Source)
			}

			// Check that codec is detected (if expected)
			if tt.expected.Codec != "" && result.Codec != tt.expected.Codec {
				t.Errorf("Codec: got %q, want %q", result.Codec, tt.expected.Codec)
			}

			t.Logf("Parsed result: %+v", result)
		})
	}
}

func TestYearDetection(t *testing.T) {
	input := "Blade.Runner.2049.2017.2160p.BluRay.HEVC.TrueHD.7.1.Atmos-COASTER"
	result := Parse(input)

	// Check if 2017 is detected as the year
	if result.Year != 2017 {
		t.Errorf("Expected year 2017, got %d", result.Year)
	}

	// Check if title includes 2049
	if !strings.Contains(result.Title, "2049") {
		t.Errorf("Expected title to contain '2049', got '%s'", result.Title)
	}
}
