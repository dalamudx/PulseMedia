package httpapi

import (
	"net/http"
	"net/url"
)

// Runs inside private cache headers and before generated query binding.
func subscriptionListQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/subscriptions" && !validSubscriptionQuery(r.URL.RawQuery) {
			WriteJSONError(w, 400, "INVALID_QUERY", "invalid subscription query", GetRequestID(r.Context()))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validSubscriptionQuery(raw string) bool {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return false
	}
	for key, v := range values {
		switch key {
		case "limit", "offset", "search", "media_type", "tmdb_id", "own_only", "view", "start", "end", "unknown_dates":
		default:
			return false
		}
		if len(v) != 1 || v[0] == "" {
			return false
		}
	}
	return true
}
