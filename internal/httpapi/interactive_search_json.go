package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func strictInteractiveSearchJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		create := r.Method == http.MethodPost && r.URL.Path == "/api/v1/release-searches"
		cancel := r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/release-searches/") && strings.HasSuffix(r.URL.Path, "/cancel")
		if !create && !cancel {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid search request", GetRequestID(r.Context()))
		}
		if r.Body == nil {
			if cancel {
				next.ServeHTTP(w, r)
			} else {
				fail()
			}
			return
		}
		defer func() { _ = r.Body.Close() }()
		if cancel {
			b, err := io.ReadAll(io.LimitReader(r.Body, 1))
			if err != nil || len(b) != 0 {
				fail()
				return
			}
			r.Body = http.NoBody
			next.ServeHTTP(w, r)
			return
		}
		var body CreateReleaseSearchJSONRequestBody
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil {
			fail()
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			fail()
			return
		}
		raw, err := json.Marshal(body)
		if err != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
