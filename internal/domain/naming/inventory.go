package naming

import (
	"math"
	"path"
	"strings"
)

// InventoryFile includes every file, including deselected files.
type InventoryFile struct {
	Index     int32   `json:"index"`
	Name      string  `json:"name"`
	SizeBytes int64   `json:"size_bytes"`
	Progress  float64 `json:"progress"`
}

func IsVideoPath(name string) bool { return videoExtensions[strings.ToLower(path.Ext(name))] }

// MovieDirectoryVideo requires a complete, bounded inventory with one primary video.
// Every reported file must be inside the reported directory under the original save root.
func MovieDirectoryVideo(save, directory string, files []InventoryFile, total int64) (InventoryFile, string, bool) {
	var selected InventoryFile
	if !path.IsAbs(save) || path.Clean(save) != save || !path.IsAbs(directory) || path.Clean(directory) != directory || (directory != save && !strings.HasPrefix(directory, strings.TrimSuffix(save, "/")+"/")) || len(files) == 0 || len(files) > 1000 || total <= 0 {
		return selected, "", false
	}
	names := map[string]bool{}
	indices := map[int32]bool{}
	var sum int64
	count := 0
	result := ""
	for _, f := range files {
		if f.Index < 0 || indices[f.Index] || names[f.Name] || f.Name == "" || len(f.Name) > 1024 || path.Clean(f.Name) != f.Name || path.IsAbs(f.Name) || f.Name == "." || f.Name == ".." || strings.HasPrefix(f.Name, "../") || strings.ContainsAny(f.Name, "\\:\x00\r\n") || strings.Contains(strings.ToLower(f.Name), "token=") || strings.Contains(strings.ToLower(f.Name), "passkey=") || f.SizeBytes < 0 || f.SizeBytes > math.MaxInt64-sum || f.Progress != 1 {
			return selected, "", false
		}
		full := path.Join(save, f.Name)
		if !strings.HasPrefix(full, strings.TrimSuffix(directory, "/")+"/") {
			return selected, "", false
		}
		names[f.Name] = true
		indices[f.Index] = true
		sum += f.SizeBytes
		if IsVideoPath(f.Name) {
			count++
			selected = f
			result = full
		}
	}
	eligible, _ := IsEligibleMediaFile(selected.Name, selected.SizeBytes, 1)
	return selected, result, sum == total && count == 1 && selected.SizeBytes > 0 && eligible
}
