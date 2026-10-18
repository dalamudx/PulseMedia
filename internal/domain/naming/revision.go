package naming

import (
	"path/filepath"
	"strings"
)

// ValidManualTarget accepts only an already canonical relative file path.
func ValidManualTarget(target string) bool {
	clean, e := CleanRelPath(target)
	return e == nil && clean == target && target != "." && len(target) <= 1024 && !strings.ContainsAny(target, "\\\x00\r\n:?<>|\"*#") && filepath.Ext(target) != ""
}
