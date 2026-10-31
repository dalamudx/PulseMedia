package httpapi

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func strictDeliveryListQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/deliveries" || r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		actor := GetActor(r.Context())
		if actor == nil || !actor.IsAdmin() {
			e := ports.ErrForbidden
			if actor == nil {
				e = ports.ErrUnauthorized
			}
			_ = deliveryFailure(r.Context(), e).VisitListIntegrationDeliveriesResponse(w)
			return
		}
		fail := func() {
			_ = deliveryFailure(r.Context(), ports.ErrInvalidDeliveryCommand).VisitListIntegrationDeliveriesResponse(w)
		}
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || r.URL.RawPath != "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.Header.Values("Idempotency-Key")) != 0 {
			fail()
			return
		}
		for key, values := range q {
			if (key != "state" && key != "cursor" && key != "limit" && key != "event_id") || len(values) != 1 {
				fail()
				return
			}
		}
		if q.Has("event_id") {
			id, e := uuid.Parse(q.Get("event_id"))
			if e != nil || id == uuid.Nil {
				fail()
				return
			}
		}
		if len(q.Get("cursor")) > 512 {
			fail()
			return
		}
		switch q.Get("state") {
		case "", "pending", "delivering", "delivered", "dead", "skipped":
		default:
			fail()
			return
		}
		if q.Has("limit") {
			n, e := strconv.Atoi(q.Get("limit"))
			if e != nil || n < 1 || n > 100 {
				fail()
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
