package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func strictTransferRecoveryJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/api/v1/transfer-plans/") || !strings.HasSuffix(r.URL.Path, "/recovery") {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() { WriteJSONError(w, 400, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context())) }
		if r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		var body CommitOriginalTransferRecoveryJSONRequestBody
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || !bool(body.Confirmed) || len(body.ExpectedInputStamp) != 64 || strings.TrimSpace(body.Reason) == "" || len([]rune(body.Reason)) > 240 {
			fail()
			return
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			fail()
			return
		}
		for _, ch := range body.ExpectedInputStamp {
			if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
				fail()
				return
			}
		}
		raw, e := json.Marshal(body)
		if e != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
