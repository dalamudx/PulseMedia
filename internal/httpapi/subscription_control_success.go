package httpapi

import (
	"encoding/json"
	"net/http"
)

type controlSuccess struct {
	Body ControlSubscription200JSONResponse
}

func (s controlSuccess) VisitControlSubscriptionResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(s.Body)
}
