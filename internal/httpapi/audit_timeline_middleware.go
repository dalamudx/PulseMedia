package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/auditlog"
)

const auditTimelinePath = "/api/v1/system/audit"

func auditTimelineNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auditTimelinePath {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func strictAuditTimelineQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == auditTimelinePath && r.Method == http.MethodGet && auditTimelineReadAllowed(GetActor(r.Context())) && !validAuditTimelineQuery(r.URL.RawQuery) {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid audit filter or cursor", GetRequestID(r.Context()))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validAuditTimelineQuery(raw string) bool {
	if len(raw) > 8192 {
		return false
	}
	query, err := url.ParseQuery(raw)
	if err != nil {
		return false
	}
	var filter auditlog.Filter
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			return false
		}
		value := values[0]
		switch key {
		case "limit":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > auditlog.MaximumLimit || strconv.Itoa(n) != value {
				return false
			}
		case "cursor":
			if len(value) > 1024 {
				return false
			}
		case "actor_type":
			filter.ActorType = value
		case "actor_id":
			filter.ActorID = value
		case "action":
			filter.Action = value
		case "target_type":
			filter.TargetType = value
		case "target_id":
			filter.TargetID = value
		case "from", "until":
			stamp, err := time.Parse(time.RFC3339Nano, value)
			if err != nil || stamp.IsZero() {
				return false
			}
			if key == "from" {
				filter.From = stamp
			} else {
				filter.Until = stamp
			}
		default:
			return false
		}
	}
	_, err = auditlog.Normalize(filter)
	return err == nil
}
