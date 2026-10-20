package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func siteAccountStatsConfigRoute(path string) bool {
	return strings.HasPrefix(path, "/api/v1/indexers/") && strings.Contains(path, "/account-stats/config")
}
func siteAccountStatsConfigNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if siteAccountStatsConfigRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func validSiteAccountStatsConfigInput(in ports.SiteAccountStatsConfigInput) bool {
	id, err := strconv.ParseUint(in.ExpectedAccountID, 10, 64)
	return err == nil && id > 0 && strconv.FormatUint(id, 10) == in.ExpectedAccountID && in.Profile == "nexusphp_classic_info_block_v1" && in.IntervalSeconds >= 900 && in.IntervalSeconds <= 86400 && in.ExpectedVersion >= 0 && in.ExpectedSourceVersion >= 1
}
func strictSiteAccountStatsConfigJSON(next http.Handler) http.Handler {
	return siteAccountStatsConfigNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !siteAccountStatsConfigRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if err := siteAccountStatsConfigActorError(GetActor(r.Context())); err != nil {
			p := siteAccountStatsConfigProblem(r.Context(), err)
			writeProblem(w, p.problem.Status, p.problem)
			return
		}
		fail := func() {
			WriteJSONError(w, 400, "INVALID_REQUEST", "invalid account statistics configuration request", GetRequestID(r.Context()))
		}
		tail := strings.TrimPrefix(r.URL.Path, "/api/v1/indexers/")
		parts := strings.Split(tail, "/")
		if len(parts) != 3 && len(parts) != 5 {
			fail()
			return
		}
		id, err := uuid.Parse(parts[0])
		if err != nil || id == uuid.Nil || parts[1] != "account-stats" || parts[2] != "config" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			fail()
			return
		}
		if len(parts) == 5 && (parts[3] != "commands" || !validDoubanSyncCommandKey(parts[4])) {
			fail()
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if r.Method == http.MethodGet {
			if len(keys) != 0 {
				fail()
				return
			}
			if r.Body != nil {
				defer func() { _ = r.Body.Close() }()
				b, e := io.ReadAll(io.LimitReader(r.Body, 1))
				if e != nil || len(b) != 0 {
					fail()
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPut || len(parts) != 3 {
			next.ServeHTTP(w, r)
			return
		}
		if len(keys) != 1 || !validDoubanSyncCommandKey(keys[0]) || r.Body == nil {
			fail()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 4095))
		if err != nil {
			fail()
			return
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		fields := map[string]bool{"expected_account_id": true, "profile": true, "enabled": true, "interval_seconds": true, "expected_version": true, "expected_source_version": true}
		if !uniqueEndpointJSONValue(d, 0, fields) {
			fail()
			return
		}
		if _, err = d.Token(); err != io.EOF {
			fail()
			return
		}
		var in ports.SiteAccountStatsConfigInput
		d = json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || !validSiteAccountStatsConfigInput(in) {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	}))
}
