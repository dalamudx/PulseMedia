package postgres

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type libraryRecycleCursor struct {
	Version int    `json:"v"`
	After   string `json:"after"`
	Kind    string `json:"kind"`
	Query   string `json:"q"`
}

func validLibraryRecycleID(value string) bool {
	kind, raw, ok := strings.Cut(value, ":")
	if !ok || (kind != "movie" && kind != "tv") {
		return false
	}
	id, err := uuid.Parse(raw)
	return err == nil && id != uuid.Nil && id.String() == raw
}

func decodeLibraryRecycleCursor(q ports.LibraryRecycleQuery) (string, error) {
	if q.Limit < 1 || q.Limit > 100 || len(q.Cursor) > 2048 || (q.Kind != "" && q.Kind != "movie" && q.Kind != "tv") || !utf8.ValidString(q.Query) || strings.ContainsRune(q.Query, 0) || utf8.RuneCountInString(q.Query) > 128 {
		return "", ports.ErrRejected
	}
	if q.Cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != q.Cursor {
		return "", ports.ErrRejected
	}
	// Existing unfiltered cursors remain valid; they cannot cross into a filter.
	if q.Kind == "" && q.Query == "" && validLibraryRecycleID(string(raw)) {
		return string(raw), nil
	}
	var value libraryRecycleCursor
	if json.Unmarshal(raw, &value) != nil || value.Version != 1 || value.Kind != q.Kind || value.Query != q.Query || !validLibraryRecycleID(value.After) {
		return "", ports.ErrRejected
	}
	canonical, _ := json.Marshal(value)
	if string(canonical) != string(raw) {
		return "", ports.ErrRejected
	}
	return value.After, nil
}

func encodeLibraryRecycleCursor(after string, q ports.LibraryRecycleQuery) string {
	if q.Kind == "" && q.Query == "" {
		return base64.RawURLEncoding.EncodeToString([]byte(after))
	}
	raw, _ := json.Marshal(libraryRecycleCursor{Version: 1, After: after, Kind: q.Kind, Query: q.Query})
	return base64.RawURLEncoding.EncodeToString(raw)
}
