package postgres

import (
	"encoding/base64"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"strings"
	"testing"
)

func TestLibraryMaintenanceRecycleCursor(t *testing.T) {
	id := "movie:00000000-0000-0000-0000-000000000001"
	q := ports.LibraryRecycleQuery{Limit: 20, Kind: "movie", Query: "目录/%_\\片名"}
	q.Cursor = encodeLibraryRecycleCursor(id, q)
	if after, e := decodeLibraryRecycleCursor(q); e != nil || after != id {
		t.Fatal(after, e)
	}
	for name, change := range map[string]func(*ports.LibraryRecycleQuery){
		"different kind":   func(q *ports.LibraryRecycleQuery) { q.Kind = "tv" },
		"different query":  func(q *ports.LibraryRecycleQuery) { q.Query = "other" },
		"dropped filters":  func(q *ports.LibraryRecycleQuery) { q.Kind = ""; q.Query = "" },
		"padded base64":    func(q *ports.LibraryRecycleQuery) { q.Cursor += "=" },
		"unsupported kind": func(q *ports.LibraryRecycleQuery) { q.Kind = "all" },
		"too long query":   func(q *ports.LibraryRecycleQuery) { q.Query = strings.Repeat("片", 129) },
		"invalid utf8":     func(q *ports.LibraryRecycleQuery) { q.Query = string([]byte{255}) },
		"nul":              func(q *ports.LibraryRecycleQuery) { q.Query = "\x00" },
		"oversized cursor": func(q *ports.LibraryRecycleQuery) { q.Cursor = strings.Repeat("A", 2049) },
		"zero limit":       func(q *ports.LibraryRecycleQuery) { q.Limit = 0 },
		"large limit":      func(q *ports.LibraryRecycleQuery) { q.Limit = 101 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := q
			change(&bad)
			if _, e := decodeLibraryRecycleCursor(bad); !errors.Is(e, ports.ErrRejected) {
				t.Fatal(e)
			}
		})
	}
	legacy := ports.LibraryRecycleQuery{Limit: 1, Cursor: base64.RawURLEncoding.EncodeToString([]byte(id))}
	if after, e := decodeLibraryRecycleCursor(legacy); e != nil || after != id {
		t.Fatal("legacy", after, e)
	}
	legacy.Kind = "movie"
	if _, e := decodeLibraryRecycleCursor(legacy); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("unbound legacy", e)
	}
	for name, raw := range map[string]string{
		"extra field":      `{"v":1,"after":"` + id + `","kind":"movie","q":"x","extra":1}`,
		"wrong version":    `{"v":2,"after":"` + id + `","kind":"movie","q":"x"}`,
		"invalid position": `{"v":1,"after":"movie:invalid","kind":"movie","q":"x"}`,
		"trailing JSON":    `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			bad := ports.LibraryRecycleQuery{Limit: 1, Kind: "movie", Query: "x", Cursor: base64.RawURLEncoding.EncodeToString([]byte(raw))}
			if _, e := decodeLibraryRecycleCursor(bad); !errors.Is(e, ports.ErrRejected) {
				t.Fatal(e)
			}
		})
	}
}
