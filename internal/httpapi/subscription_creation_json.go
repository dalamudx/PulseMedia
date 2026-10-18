package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

func strictSubscriptionCreationJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/subscriptions" {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid subscription request", GetRequestID(r.Context()))
		}
		if r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		var body CreateSubscriptionJSONRequestBody
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
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
