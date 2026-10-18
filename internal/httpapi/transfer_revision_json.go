package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func strictTransferRevisionJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		preview := strings.HasSuffix(r.URL.Path, "/revision-preview")
		command := strings.HasSuffix(r.URL.Path, "/revision")
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/api/v1/transfer-plans/") || (!preview && !command) {
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
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		var value any
		if preview {
			var body PreviewTransferRevisionJSONRequestBody
			if d.Decode(&body) != nil || body.TargetRel == "" || len(body.TargetRel) > 1024 || body.Action != nil && !body.Action.Valid() {
				fail()
				return
			}
			value = body
		} else {
			var body CommitTransferRevisionJSONRequestBody
			if d.Decode(&body) != nil || body.TargetRel == "" || len(body.TargetRel) > 1024 || !bool(body.Confirmed) || len(body.ExpectedInputStamp) != 64 || strings.TrimSpace(body.Reason) == "" || len([]rune(body.Reason)) > 240 || body.Action != nil && !body.Action.Valid() {
				fail()
				return
			}
			for _, ch := range body.ExpectedInputStamp {
				if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
					fail()
					return
				}
			}
			value = body
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			fail()
			return
		}
		raw, err := json.Marshal(value)
		if err != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
