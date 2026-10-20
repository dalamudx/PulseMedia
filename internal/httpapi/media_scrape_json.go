package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

func strictMediaScrapeJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/media/scrape" {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/media/scrape" {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "all must be true", GetRequestID(r.Context()))
		}
		if r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		token, err := d.Token()
		if err != nil || token != json.Delim('{') {
			fail()
			return
		}
		name, err := d.Token()
		if err != nil || name != "all" {
			fail()
			return
		}
		value, err := d.Token()
		if err != nil || value != true || d.More() {
			fail()
			return
		}
		token, err = d.Token()
		if err != nil || token != json.Delim('}') {
			fail()
			return
		}
		if d.Decode(new(any)) != io.EOF {
			fail()
			return
		}
		raw := []byte(`{"all":true}`)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
