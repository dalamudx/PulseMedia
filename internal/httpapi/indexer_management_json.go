package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func strictIndexerManagementJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const root = "/api/v1/indexers"
		var body any
		switch {
		case r.Method == http.MethodPost && r.URL.Path == root:
			body = &CreateIndexerJSONRequestBody{}
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, root+"/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, root+"/"), "/"):
			body = &UpdateIndexerJSONRequestBody{}
		default:
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context()))
		}
		if r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(body) != nil {
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
