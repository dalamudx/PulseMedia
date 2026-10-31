package httpapi

import (
	"net/http"
	"strings"
)

func (s *ServerHandler) SetTVEpisodeUpgradeService(v TVEpisodeUpgradeApplication) {
	s.tvEpisodeUpgradeSvc = v
}

func tvEpisodeUpgradeNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/subscriptions/") && strings.Contains(r.URL.Path, "/tv-episode-upgrade-") {
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Add("Vary", "Cookie")
			w.Header().Add("Vary", "Authorization")
		}
		next.ServeHTTP(w, r)
	})
}
