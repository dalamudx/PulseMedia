package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const doubanSyncConfigPath = "/api/v1/sync/douban/config"

func doubanSyncConfigRoute(path string) bool {
	return path == doubanSyncConfigPath || strings.HasPrefix(path, doubanSyncConfigPath+"/commands/")
}

// Wrap authentication as well, so rejected credentials cannot cache configuration responses.
func doubanSyncConfigNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if doubanSyncConfigRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func validDoubanSyncCommandKey(key string) bool {
	if len(key) < 1 || len(key) > 160 {
		return false
	}
	for _, c := range []byte(key) {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '-' || c == '_' || c == '.'
		if !valid {
			return false
		}
	}
	return true
}

func strictDoubanSyncConfigJSON(next http.Handler) http.Handler {
	return doubanSyncConfigNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !doubanSyncConfigRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			WriteJSONError(w, 400, "INVALID_REQUEST", "invalid Douban configuration request", GetRequestID(r.Context()))
		}
		if r.URL.RawQuery != "" {
			fail()
			return
		}
		if r.Method == http.MethodGet {
			if r.Body != nil {
				defer func() { _ = r.Body.Close() }()
				raw, err := io.ReadAll(io.LimitReader(r.Body, 1))
				if err != nil || len(raw) != 0 {
					fail()
					return
				}
			}
			if r.URL.Path != doubanSyncConfigPath && !validDoubanSyncCommandKey(strings.TrimPrefix(r.URL.Path, doubanSyncConfigPath+"/commands/")) {
				fail()
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPut || r.URL.Path != doubanSyncConfigPath {
			next.ServeHTTP(w, r)
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || strings.Contains(keys[0], ",") || !validDoubanSyncCommandKey(keys[0]) || r.Body == nil {
			fail()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 4095))
		if err != nil {
			fail()
			return
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		fields := map[string]bool{"douban_uid": true, "enabled": true, "expected_version": true, "movie_root_id": false, "tv_root_id": false, "bindings": false}
		if !uniqueEndpointJSONValue(d, 0, fields) {
			fail()
			return
		}
		if _, err = d.Token(); err != io.EOF {
			fail()
			return
		}
		var nested struct {
			Bindings json.RawMessage `json:"bindings"`
		}
		if json.Unmarshal(raw, &nested) != nil {
			fail()
			return
		}
		if nested.Bindings != nil && !uniqueEndpointJSONValue(json.NewDecoder(bytes.NewReader(nested.Bindings)), 0, map[string]bool{"indexer_ids": true, "downloader_id": false}) {
			fail()
			return
		}
		var in ports.DoubanSyncInput
		d = json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || !validDoubanSyncUID(in.DoubanUID) || !validDoubanSyncConfigBindings(in.Bindings) || in.MovieRootID != nil && *in.MovieRootID == [16]byte{} || in.TVRootID != nil && *in.TVRootID == [16]byte{} {
			fail()
			return
		}
		canonical, err := json.Marshal(in)
		if err != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(canonical))
		r.ContentLength = int64(len(canonical))
		next.ServeHTTP(w, r)
	}))
}

func validDoubanSyncConfigBindings(b *ports.DoubanSyncBindings) bool {
	if b == nil {
		return true
	}
	if b.IndexerIDs == nil || len(b.IndexerIDs) > 64 || (len(b.IndexerIDs) == 0) != (b.DownloaderID == nil) || b.DownloaderID != nil && *b.DownloaderID == [16]byte{} {
		return false
	}
	seen := make(map[[16]byte]bool, len(b.IndexerIDs))
	for _, id := range b.IndexerIDs {
		if id == [16]byte{} || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func validDoubanSyncUID(uid string) bool {
	if len(uid) < 1 || len(uid) > 64 {
		return false
	}
	for i, c := range []byte(uid) {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && (c == '_' || c == '-' || c == '.')
		if !valid {
			return false
		}
	}
	return true
}
