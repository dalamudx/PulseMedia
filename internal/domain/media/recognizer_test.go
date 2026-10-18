package media_test

import (
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

// Synthetic boundary controls, not independently reviewed golden inputs.
func TestNoYearReleaseLanguageSuffixPreservesWorkTitle(t *testing.T) {
	for _, c := range []struct {
		raw, title string
		year       int
	}{
		{"The.French.Connection.FRENCH.1080p.BluRay.x264", "The French Connection", 0},
		{"The.French.Connection.TRUEFRENCH.MULTI.720p.x264", "The French Connection", 0},
		{"French", "French", 0},
		{"French.720p.x264", "French", 0},
		{"The.French.Connection.FRENCH", "The French Connection FRENCH", 0},
		{"La.French.2014.FRENCH.720p.x264", "La French", 2014},
		{"22.Jump.Street.GERMAN.DL.AC3.Dubbed.1080p.BluRay.x264-SOV", "22 Jump Street", 0},
		{"The.German.Story.720p.x264", "The German Story", 0},
		{"German.720p.x264", "German", 0},
		{"DL", "DL", 0},
	} {
		t.Run(c.raw, func(t *testing.T) {
			got := media.RecognizeRelease(c.raw)
			if got.Title != c.title || got.Year != c.year {
				t.Fatalf("work title changed: %+v", got)
			}
			if c.year == 0 && (!got.RequiresReview || len(got.UnitKeys) != 0) {
				t.Fatalf("language invented identity: %+v", got)
			}
		})
	}
}

func TestRecognizeStandardReleases(t *testing.T) {
	tests := []struct {
		filename           string
		expectedTitle      string
		expectedYear       int
		expectedRes        quality.Resolution
		expectedSource     quality.Source
		expectedCodec      quality.Codec
		expectedConfidence float64
		expectReview       bool
	}{
		{
			filename:           "Fight.Club.1999.1080p.BluRay.x264-SPARKS.mkv",
			expectedTitle:      "Fight Club",
			expectedYear:       1999,
			expectedRes:        quality.Res1080p,
			expectedSource:     quality.SourceBluray,
			expectedCodec:      quality.CodecH264,
			expectedConfidence: 0.9,
			expectReview:       false,
		},
		{
			filename:           "Inception (2010) [2160p UHD Remux HEVC DV].mkv",
			expectedTitle:      "Inception",
			expectedYear:       2010,
			expectedRes:        quality.Res2160p,
			expectedSource:     quality.SourceRemux,
			expectedCodec:      quality.CodecHEVC,
			expectedConfidence: 0.9,
			expectReview:       false,
		},
		{
			filename:           "The Matrix (1999) {tmdb-603}.mp4",
			expectedTitle:      "The Matrix",
			expectedYear:       1999,
			expectedConfidence: 1.0, // Identity tag gives 1.0
			expectReview:       false,
		},
		{
			filename:           "Old_Home_Video_No_Year.avi",
			expectedTitle:      "Old Home Video No Year",
			expectedYear:       0,
			expectedConfidence: 0.6, // Missing year requires review
			expectReview:       true,
		},
	}

	for _, tt := range tests {
		rec := media.Recognize(tt.filename)
		if rec.Title != tt.expectedTitle {
			t.Errorf("[%s] Title: got %q, expected %q", tt.filename, rec.Title, tt.expectedTitle)
		}
		if rec.Year != tt.expectedYear {
			t.Errorf("[%s] Year: got %d, expected %d", tt.filename, rec.Year, tt.expectedYear)
		}
		if tt.expectedRes != "" && rec.Quality.Resolution != tt.expectedRes {
			t.Errorf("[%s] Resolution: got %q, expected %q", tt.filename, rec.Quality.Resolution, tt.expectedRes)
		}
		if tt.expectedSource != "" && rec.Quality.Source != tt.expectedSource {
			t.Errorf("[%s] Source: got %q, expected %q", tt.filename, rec.Quality.Source, tt.expectedSource)
		}
		if tt.expectedCodec != "" && rec.Quality.Codec != tt.expectedCodec {
			t.Errorf("[%s] Codec: got %q, expected %q", tt.filename, rec.Quality.Codec, tt.expectedCodec)
		}
		if rec.Confidence != tt.expectedConfidence {
			t.Errorf("[%s] Confidence: got %v, expected %v", tt.filename, rec.Confidence, tt.expectedConfidence)
		}
		if rec.RequiresReview != tt.expectReview {
			t.Errorf("[%s] RequiresReview: got %v, expected %v", tt.filename, rec.RequiresReview, tt.expectReview)
		}
	}
}

func TestRecognizeExplicitIdentities(t *testing.T) {
	// TMDB tag
	recTMDB := media.Recognize("Interstellar (2014) {tmdb-157336}.mkv")
	if recTMDB.Identity == nil || recTMDB.Identity.Namespace != "tmdb.movie" || recTMDB.Identity.ExternalID != "157336" {
		t.Fatalf("failed to extract TMDB identity: %+v", recTMDB.Identity)
	}

	// IMDb tag
	recIMDb := media.Recognize("Pulp.Fiction.1994.[imdb-tt0110912].1080p.mkv")
	if recIMDb.Identity == nil || recIMDb.Identity.Namespace != "imdb" || recIMDb.Identity.ExternalID != "tt0110912" {
		t.Fatalf("failed to extract IMDb identity: %+v", recIMDb.Identity)
	}
}
