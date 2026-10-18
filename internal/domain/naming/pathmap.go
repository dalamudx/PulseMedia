package naming

import (
	"path/filepath"
	"sort"
	"strings"
)

// TranslateRemotePath chooses the longest complete path prefix deterministically.
// Invalid or conflicting configuration fails closed; unmapped paths remain
// subject to the caller's configured-root authorization.
func TranslateRemotePath(mapping map[string]string, remote string) string {
	if !filepath.IsAbs(remote) || strings.ContainsRune(remote, '\x00') {
		return ""
	}
	for _, part := range strings.Split(filepath.ToSlash(remote), "/") {
		if part == ".." {
			return ""
		}
	}
	canonical := make(map[string]string, len(mapping))
	for key, local := range mapping {
		prefix := strings.TrimRight(key, "/")
		if key == "/" {
			prefix = "/"
		}
		if !filepath.IsAbs(prefix) || filepath.Clean(prefix) != prefix || strings.ContainsRune(prefix, '\x00') || !filepath.IsAbs(local) || filepath.Clean(local) != local || strings.ContainsRune(local, '\x00') {
			return ""
		}
		if previous, exists := canonical[prefix]; exists && previous != local {
			return ""
		}
		canonical[prefix] = local
	}
	keys := make([]string, 0, len(canonical))
	for key := range canonical {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, prefix := range keys {
		if prefix != "/" && remote != prefix && !strings.HasPrefix(remote, prefix+"/") {
			continue
		}
		return filepath.Join(canonical[prefix], strings.TrimPrefix(remote, prefix))
	}
	return remote
}
