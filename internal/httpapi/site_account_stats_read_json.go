package httpapi

import (
	"io"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func siteAccountStatsAmountResponse(a *indexer.SiteAccountAmount) *SiteAccountStatsAmount {
	if a == nil {
		return nil
	}
	return &SiteAccountStatsAmount{Value: a.Value, Unit: SiteAccountStatsAmountUnit(a.Unit), Approximate: SiteAccountStatsAmountApproximate(a.Approximate)}
}

func siteAccountStatsReadRoute(path string) bool {
	const prefix = "/api/v1/indexers/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	return len(parts) == 2 && parts[1] == "account-stats"
}

func siteAccountStatsReadNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if siteAccountStatsReadRoute(r.URL.Path) {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func strictSiteAccountStatsReadJSON(next http.Handler) http.Handler {
	return siteAccountStatsReadNoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !siteAccountStatsReadRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if err := siteAccountStatsConfigActorError(GetActor(r.Context())); err != nil {
			p := siteAccountStatsReadProblem(r.Context(), err)
			writeProblem(w, p.problem.Status, p.problem)
			return
		}
		fail := func() {
			p := siteAccountStatsReadProblem(r.Context(), ports.ErrRejected)
			writeProblem(w, p.problem.Status, p.problem)
		}
		rawID := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/indexers/"), "/")[0]
		id, err := uuid.Parse(rawID)
		if err != nil || id == uuid.Nil || id.String() != rawID || r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.Header.Values("Idempotency-Key")) != 0 {
			fail()
			return
		}
		if r.Body != nil {
			defer func() { _ = r.Body.Close() }()
			b, err := io.ReadAll(io.LimitReader(r.Body, 1))
			if err != nil || len(b) != 0 {
				fail()
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
}
