package httpapi

import (
	"context"
	"net/http"
	"net/url"
)

type activityStreamResponse struct {
	hub *SSEHub
	ctx context.Context
}

func (r activityStreamResponse) VisitGetActivityStreamResponse(w http.ResponseWriter) error {
	req := (&http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/v1/stream"}}).WithContext(r.ctx)
	r.hub.ServeHTTP(w, req)
	return nil
}
func (s *ServerHandler) GetActivityStream(ctx context.Context, _ GetActivityStreamRequestObject) (GetActivityStreamResponseObject, error) {
	if s.activityHub == nil {
		return GetActivityStream503JSONResponse{Code: "UNAVAILABLE", Message: "activity stream unavailable", RequestId: GetRequestID(ctx)}, nil
	}
	return activityStreamResponse{hub: s.activityHub, ctx: ctx}, nil
}
