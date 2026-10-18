package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"net/http"
	"strings"
)

// The generated decoder otherwise discards unknown fields before the strict handler sees them.
func strictEpisodeAcquisitionJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/episode-acquisitions") {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() { WriteJSONError(w, 400, "INVALID_REQUEST", "invalid request", GetRequestID(r.Context())) }
		if r.Body == nil {
			fail()
			return
		}
		defer func() { _ = r.Body.Close() }()
		var body AcquireReviewedEpisodeJSONRequestBody
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || body.ReviewId == uuid.Nil || !bool(body.Confirmed) || len(body.ExpectedInputStamp) != 64 {
			fail()
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
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
