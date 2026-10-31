package httpapi

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const fileMappingOptionsPath = "/api/v1/library/mapping-options"

func validFileMappingOptionsQuery(r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Header.Values("Idempotency-Key")) != 0 {
		return false
	}
	if r.Body != nil && r.Body != http.NoBody {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(body) != 0 {
			return false
		}
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			return false
		}
		switch key {
		case "root_id", "media_id":
			id, err := uuid.Parse(values[0])
			if err != nil || id == uuid.Nil || id.String() != values[0] {
				return false
			}
		case "cursor", "media_cursor":
			if len(values[0]) > 512 || strings.IndexFunc(values[0], func(r rune) bool {
				return !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", r)
			}) != -1 {
				return false
			}
		case "media_query":
			if !(ports.FileMappingOptionsQuery{MediaQuery: values[0]}).Valid() {
				return false
			}
		case "media_type":
			if values[0] != "movie" && values[0] != "tv" {
				return false
			}
		case "tmdb_id":
			id, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || id <= 0 || strconv.FormatInt(id, 10) != values[0] {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func strictFileMappingOptions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == fileMappingOptionsPath && !validFileMappingOptionsQuery(r) {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_FILE_MAPPING", "invalid file options request", GetRequestID(r.Context()))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func fileMappingOptionsNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == fileMappingOptionsPath {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
