package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const mediaRequestsPrefix = "/api/v1/media-requests"

func mediaRequestsRoute(path string) bool {
	return path == mediaRequestsPrefix || strings.HasPrefix(path, mediaRequestsPrefix+"/")
}

// MediaRequestsNoStore belongs outside authentication and generated binding.
func MediaRequestsNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mediaRequestsRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func validMediaRequestKey(key string) bool {
	if len(key) < 1 || len(key) > 80 {
		return false
	}
	for i := range len(key) {
		c := key[i]
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '.' || c == '-'
		if !allowed {
			return false
		}
	}
	return true
}

func validMediaRequestIntentTransport(in mediarequest.Intent) bool {
	return (in.MediaType == "movie" || in.MediaType == "tv") && in.TMDBID >= 1 && in.TMDBID <= 9007199254740991 && len(in.Seasons) <= 64
}

func validMediaRequestCloseTransport(in ports.MediaRequestCloseInput) bool {
	return in.ExpectedVersion >= 1 && (in.Action == "reject" || in.Action == "withdraw") && utf8.ValidString(in.Reason) && utf8.RuneCountInString(in.Reason) <= 512
}

func validMediaRequestApprovalTransport(in ports.MediaRequestApprovalInput) bool {
	if in.ExpectedVersion < 1 || in.RootID == uuid.Nil || in.DownloaderID == nil || *in.DownloaderID == uuid.Nil || len(in.IndexerIDs) < 1 || len(in.IndexerIDs) > 64 {
		return false
	}
	for _, id := range in.IndexerIDs {
		if id == uuid.Nil {
			return false
		}
	}
	return true
}

func validMediaRequestUUID(raw string) bool {
	id, err := uuid.Parse(raw)
	return err == nil && id != uuid.Nil && id.String() == raw
}

// StrictMediaRequestJSON runs after AuthMiddleware, before generated binding.
// It gates every family route before parsing any caller-controlled JSON or cursor.
func StrictMediaRequestJSON(next http.Handler) http.Handler {
	return MediaRequestsNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mediaRequestsRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, mediaRequestsPrefix+"/"), "/")
		admin := len(parts) == 2 && parts[1] == "approve" || len(parts) >= 2 && parts[1] == "approval-commands"
		if err := mediaRequestActorError(GetActor(r.Context()), admin); err != nil {
			_ = mediaRequestError(r.Context(), err).write(w)
			return
		}
		fail := func() { _ = mediaRequestBadRequest(r.Context()).write(w) }
		summary := r.URL.Path == mediaRequestsPrefix+"/summary"
		root := r.URL.Path == mediaRequestsPrefix || summary
		list := root && !summary && r.Method == http.MethodGet
		if !validMediaRequestQuery(r, list) {
			fail()
			return
		}
		if !root {
			switch {
			case len(parts) == 2 && parts[0] == "commands":
				if !validMediaRequestKey(parts[1]) {
					fail()
					return
				}
			case len(parts) == 3 && parts[1] == "approval-commands":
				if !validMediaRequestUUID(parts[0]) || !validMediaRequestKey(parts[2]) {
					fail()
					return
				}
			case len(parts) == 1 || len(parts) == 2 && (parts[1] == "close" || parts[1] == "approve"):
				if !validMediaRequestUUID(parts[0]) {
					fail()
					return
				}
			default:
				next.ServeHTTP(w, r)
				return
			}
		}
		mutation := root || len(parts) == 2 && (parts[1] == "close" || parts[1] == "approve")
		if r.Method != http.MethodPost || !mutation {
			next.ServeHTTP(w, r)
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || !validMediaRequestKey(keys[0]) || r.Body == nil {
			fail()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 32<<10))
		if err != nil || !utf8.Valid(raw) {
			fail()
			return
		}
		fields := map[string]bool{"media_type": true, "tmdb_id": true, "seasons": true, "include_specials": true}
		var body any = &mediarequest.Intent{}
		if !root && parts[1] == "close" {
			fields = map[string]bool{"expected_version": true, "action": true, "reason": true}
			body = &ports.MediaRequestCloseInput{}
		} else if !root {
			fields = map[string]bool{"expected_version": true, "root_id": true, "quality_policy_id": true, "indexer_ids": true, "downloader_id": true}
			body = &ports.MediaRequestApprovalInput{}
		}
		tokens := json.NewDecoder(bytes.NewReader(raw))
		tokens.UseNumber()
		if !uniqueEndpointJSONValue(tokens, 0, fields) {
			fail()
			return
		}
		if _, err = tokens.Token(); err != io.EOF {
			fail()
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(body) != nil {
			fail()
			return
		}
		valid := false
		switch in := body.(type) {
		case *mediarequest.Intent:
			valid = validMediaRequestIntentTransport(*in)
		case *ports.MediaRequestCloseInput:
			valid = validMediaRequestCloseTransport(*in)
		case *ports.MediaRequestApprovalInput:
			valid = validMediaRequestApprovalTransport(*in)
		}
		if !valid {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	}))
}

func validMediaRequestQuery(r *http.Request, list bool) bool {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !list && len(q) != 0 {
		return false
	}
	for name, values := range q {
		if len(values) != 1 {
			return false
		}
		switch name {
		case "limit":
			n, err := strconv.Atoi(values[0])
			if err != nil || n < 1 || n > 100 || strconv.Itoa(n) != values[0] {
				return false
			}
		case "before_id":
			if !validMediaRequestUUID(values[0]) {
				return false
			}
		case "state":
			if values[0] != "pending" {
				return false
			}
		default:
			return false
		}
	}
	return true
}
