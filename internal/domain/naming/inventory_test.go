package naming

import (
	"math"
	"testing"
)

func TestMovieDirectorySelectionCompleteAndUnique(t *testing.T) {
	original := []InventoryFile{{Index: 0, Name: "release/movie.mkv", SizeBytes: 200, Progress: 1}, {Index: 1, Name: "release/subtitle.srt", SizeBytes: 8, Progress: 1}}
	f, p, ok := MovieDirectoryVideo("/downloads", "/downloads/release", original, 208)
	if !ok || f.SizeBytes != 200 || p != "/downloads/release/movie.mkv" {
		t.Fatal("valid inventory", f, p, ok)
	}
	for _, mode := range []string{"two_videos", "partial_sidecar", "escape", "outside_directory", "duplicate_index", "duplicate_path", "wrong_total", "overflow", "sample", "bad_progress"} {
		t.Run(mode, func(t *testing.T) {
			files := append([]InventoryFile(nil), original...)
			total := int64(208)
			switch mode {
			case "two_videos":
				files[1].Name = "release/another-feature.mkv"
			case "partial_sidecar":
				files[1].Progress = .5
			case "escape":
				files[1].Name = "release/../../subtitle.srt"
			case "outside_directory":
				files[1].Name = "subtitle.srt"
			case "duplicate_index":
				files[1].Index = 0
			case "duplicate_path":
				files[1].Name = files[0].Name
			case "wrong_total":
				total++
			case "overflow":
				files[1].SizeBytes = math.MaxInt64
			case "sample":
				files[0].Name = "release/trailer.mkv"
			case "bad_progress":
				files[0].Progress = math.NaN()
			}
			if _, _, ok := MovieDirectoryVideo("/downloads", "/downloads/release", files, total); ok {
				t.Fatal("unsafe directory accepted", mode)
			}
		})
	}
}
