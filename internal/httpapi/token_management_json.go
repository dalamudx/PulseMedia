package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

func strictTokenManagementJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/tokens" {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			WriteJSONError(w, 400, "INVALID_TOKEN_COMMAND", "invalid token command", GetRequestID(r.Context()))
		}
		if r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		var b CreateOwnTokenJSONRequestBody
		if d.Decode(&b) != nil {
			fail()
			return
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			fail()
			return
		}
		raw, e := json.Marshal(b)
		if e != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
