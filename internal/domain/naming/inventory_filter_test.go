package naming

import (
	"math"
	"reflect"
	"testing"
)

func TestMovieDirectoryFiltersSamplesButKeepsAllEvidence(t *testing.T) {
	original := []InventoryFile{{Index: 0, Name: "release/main.mkv", SizeBytes: 200, Progress: 1}, {Index: 1, Name: "release/sample.mkv", SizeBytes: 8, Progress: 1}, {Index: 2, Name: "release/Trailers/trailer.mp4", SizeBytes: 9, Progress: 1}, {Index: 3, Name: "release/movie.nfo", SizeBytes: 2, Progress: 1}}
	before := append([]InventoryFile(nil), original...)
	selected, full, ok := MovieDirectoryVideo("/downloads", "/downloads/release", original, 219)
	if !ok || selected.Index != 0 || selected.SizeBytes != 200 || full != "/downloads/release/main.mkv" {
		t.Fatal("primary selection", selected, full, ok)
	}
	if !reflect.DeepEqual(before, original) {
		t.Fatal("filter rewrote raw inventory")
	}
	for _, mode := range []string{"incomplete_sample", "missing_bytes", "negative_sample", "overflow_sample", "escape_sample", "duplicate_index", "duplicate_path", "another_primary", "only_excluded", "zero_main"} {
		t.Run(mode, func(t *testing.T) {
			files := append([]InventoryFile(nil), original...)
			total := int64(219)
			switch mode {
			case "incomplete_sample":
				files[1].Progress = .5
			case "missing_bytes":
				total = 211
			case "negative_sample":
				files[1].SizeBytes = -1
			case "overflow_sample":
				files[1].SizeBytes = math.MaxInt64
			case "escape_sample":
				files[1].Name = "../sample.mkv"
			case "duplicate_index":
				files[1].Index = 0
			case "duplicate_path":
				files[1].Name = files[0].Name
			case "another_primary":
				files[1].Name = "release/another-feature.mkv"
			case "only_excluded":
				files[0].Name = "release/featurette.mkv"
			case "zero_main":
				files[0].SizeBytes = 0
				total = 19
			}
			if _, _, ok := MovieDirectoryVideo("/downloads", "/downloads/release", files, total); ok {
				t.Fatal("incomplete or ambiguous evidence accepted")
			}
		})
	}
}
