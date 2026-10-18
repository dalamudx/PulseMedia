package media

import "testing"

// These hand contracts exercise declaration boundaries, independently of corpus labels.
func TestPublisherBatchIntervalDeclarations(t *testing.T) {
	for _, tt := range []struct {
		name, title, publisher string
	}{
		{"[HorribleSubs] ReLIFE (01-13) [1080p] (Batch)", "ReLIFE", "HorribleSubs"},
		{"[Cthune] Rolling Girls - Batch 09-40 [720p H264 AAC][EBDE8F5B].mkv", "Rolling Girls", "Cthune"},
		{"[BakedFish] Hagane Orchestra - 01 - 12 - Batch [720p][MP4][AAC]", "Hagane Orchestra", "BakedFish"},
		{"[Group] Madame Web - 01-12 [720p]", "Madame Web", "Group"},
		{"[Group] The Bad Batch - 01-12 [720p]", "The Bad Batch", "Group"},
		{"[Group] Batch - 01-12 [720p]", "Batch", "Group"},
		{"[Group] Batch of Dreams - 01-12 [720p]", "Batch of Dreams", "Group"},
		{"[Group] Series - 01 - 12 - (Batch) [720p].mkv", "Series", "Group"},
		{"[Group] Series - 01 - 12 - [Batch] [720p].mkv", "Series", "Group"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Run("helper", func(t *testing.T) {
				hint := publisherEpisodeRangeHint(tt.name)
				if hint == nil || hint.title != tt.title || hint.publisher != tt.publisher {
					t.Fatalf("range declaration: got %+v, want title %q publisher %q", hint, tt.title, tt.publisher)
				}
			})
			t.Run("public", func(t *testing.T) {
				got := RecognizeRelease(tt.name)
				if got.Title != tt.title || got.Quality.Group != tt.publisher {
					t.Errorf("title/publisher: got %+v, want %q / %q", got, tt.title, tt.publisher)
				}
				if got.RawName != tt.name || got.MediaType != TypeTV || got.Reason != "episode_coverage_unresolved" || !got.RequiresReview || got.Confidence != 0 || len(got.UnitKeys) != 0 || len(got.Episodes) != 0 || got.Season != 0 || got.AbsoluteEpisode != 0 || got.IsAnime {
					t.Errorf("interval must remain unresolved TV evidence with no units: %+v", got)
				}
				legacy := Recognize(tt.name)
				if legacy.RawName != tt.name || !legacy.RequiresReview || legacy.Confidence != 0 || legacy.Identity != nil {
					t.Errorf("legacy parser must not authorize interval: %+v", legacy)
				}
			})
		})
	}
}

func TestPublisherBatchIntervalAmbiguityGuards(t *testing.T) {
	for _, name := range []string{
		"[SubsPlease] Chihayafuru 3 - 21 [1080p].mkv",
		"[Group] Series - 01 - 12 [720p].mkv",
		"[Group] Series - Batch 01 - 12 [720p].mkv",
		"[Group] Series (01-12 [720p] (Batch)",
		"[Group] Series - 01 - 12 - Batchwork [720p].mkv",
		"[Group] Series - 01 - 12 - Batch of Dreams [720p].mkv",
		"[Group] Series - 01 - 12 [720p] Batch of Dreams.mkv",
		"[Group] Documentary - 1900-2020 [1080p] (Batch)",
		"[Group] Documentary - 1900 - 2020 - Batch [1080p]",
		"[Group] News - 2020-01-12 [1080p] (Batch)",
		"[Group] News - 01-12-2020 [1080p] (Batch)",
	} {
		t.Run(name, func(t *testing.T) {
			if hint := publisherEpisodeRangeHint(name); hint != nil {
				t.Fatalf("ambiguous numbers or calendar stamp became an interval: %+v", hint)
			}
		})
	}
	got := RecognizeRelease("[SubsPlease] Chihayafuru 3 - 21 [1080p].mkv")
	if got.Title != "Chihayafuru 3" || got.Quality.Group != "SubsPlease" || !got.IsAnime || got.AbsoluteEpisode != 21 || got.Reason != "absolute_mapping_required" || !got.RequiresReview || got.Confidence != 0 || got.Season != 0 || len(got.Episodes) != 0 || len(got.UnitKeys) != 0 {
		t.Fatalf("numeric title plus a single absolute episode changed: %+v", got)
	}
}
