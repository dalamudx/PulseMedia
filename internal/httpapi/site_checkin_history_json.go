package httpapi

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func siteCheckinHistoryRoute(path string) bool {
	const prefix = "/api/v1/indexers/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	return len(parts) == 3 && parts[1] == "checkin" && parts[2] == "history"
}
func siteCheckinHistoryNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if siteCheckinHistoryRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}
func strictSiteCheckinHistoryJSON(next http.Handler) http.Handler {
	return siteCheckinHistoryNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !siteCheckinHistoryRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		fail := func(err error) {
			p := siteCheckinHistoryProblem(r.Context(), err)
			writeProblem(w, p.problem.Status, p.problem)
		}
		if err := siteCheckinConfigActorError(GetActor(r.Context())); err != nil {
			fail(err)
			return
		}
		rawID := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/indexers/"), "/")[0]
		id, err := uuid.Parse(rawID)
		if err != nil || id == uuid.Nil || id.String() != rawID || r.URL.EscapedPath() != r.URL.Path || r.URL.ForceQuery {
			fail(ports.ErrRejected)
			return
		}
		for name := range r.Header {
			if strings.EqualFold(name, "Idempotency-Key") {
				fail(ports.ErrRejected)
				return
			}
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			fail(ports.ErrRejected)
			return
		}
		for _, pair := range strings.Split(r.URL.RawQuery, "&") {
			if pair == "" {
				if r.URL.RawQuery != "" {
					fail(ports.ErrRejected)
					return
				}
				continue
			}
			name, _, ok := strings.Cut(pair, "=")
			if !ok || (name != "limit" && name != "cursor") {
				fail(ports.ErrRejected)
				return
			}
		}
		for name, values := range q {
			if len(values) != 1 {
				fail(ports.ErrRejected)
				return
			}
			value := values[0]
			switch name {
			case "limit":
				n, err := strconv.ParseInt(value, 10, 32)
				if err != nil || n < 1 || n > 100 || strconv.FormatInt(n, 10) != value {
					fail(ports.ErrRejected)
					return
				}
			case "cursor":
				if value == "" || len(value) > 512 {
					fail(ports.ErrRejected)
					return
				}
			default:
				fail(ports.ErrRejected)
				return
			}
		}
		if r.Body != nil {
			defer func() { _ = r.Body.Close() }()
			b, err := io.ReadAll(io.LimitReader(r.Body, 1))
			if err != nil || len(b) != 0 {
				fail(ports.ErrRejected)
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
}
