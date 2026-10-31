package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"mime"
	"net/http"
	"strings"
)

// StrictDownloadControlJSON runs inside authentication before generated decoding.
func StrictDownloadControlJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "operations" || parts[4] != "download-control" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		fail := func() {
			writeProblem(w, 400, problemResponse(400, "INVALID_DOWNLOAD_CONTROL", "invalid download control request", GetRequestID(r.Context())))
		}
		id, e := uuid.Parse(parts[3])
		if e != nil || id == uuid.Nil || id.String() != parts[3] || r.URL.RawQuery != "" {
			fail()
			return
		}
		observation := len(parts) == 6 && parts[5] == "observations"
		if len(parts) != 5 && !observation {
			next.ServeHTTP(w, r)
			return
		}
		if (observation && r.Method != http.MethodPost) || (!observation && r.Method != http.MethodPost && r.Method != http.MethodGet) {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if len(r.Header.Values("Idempotency-Key")) != 1 || key == "" || len(key) > 160 || key != strings.TrimSpace(key) || strings.ContainsAny(key, ",\x00\r\n") {
			fail()
			return
		}
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		if r.Body == nil {
			if observation {
				next.ServeHTTP(w, r)
				return
			}
			fail()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, e := io.ReadAll(http.MaxBytesReader(w, original, 4096))
		if e != nil {
			fail()
			return
		}
		if observation {
			if len(bytes.TrimSpace(raw)) != 0 {
				fail()
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(nil))
			r.ContentLength = 0
			next.ServeHTTP(w, r)
			return
		}
		content, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || content != "application/json" || len(r.Header.Values("Content-Type")) != 1 {
			fail()
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if !uniqueEndpointJSONValue(decoder, 0, map[string]bool{"action": true, "confirmed": true, "affect_shared": false}) {
			fail()
			return
		}
		if _, e = decoder.Token(); e != io.EOF {
			fail()
			return
		}
		var command DownloadControlCommand
		if json.Unmarshal(raw, &command) != nil || !bool(command.Confirmed) || (command.Action != "pause" && command.Action != "resume") {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
