package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type siteAccountStatsApplication interface {
	Read(context.Context, *ports.Actor, uuid.UUID) (*ports.SiteAccountStatsView, error)
}

func (s *ServerHandler) SetSiteAccountStatsService(service siteAccountStatsApplication) {
	s.siteAccountStatsSvc = service
}

func (s *ServerHandler) ReadSiteAccountStats(ctx context.Context, r ReadSiteAccountStatsRequestObject) (ReadSiteAccountStatsResponseObject, error) {
	if err := siteAccountStatsConfigActorError(GetActor(ctx)); err != nil {
		return siteAccountStatsReadProblem(ctx, err), nil
	}
	if r.IndexerId == uuid.Nil {
		return siteAccountStatsReadProblem(ctx, ports.ErrRejected), nil
	}
	if s.siteAccountStatsSvc == nil {
		return siteAccountStatsReadProblem(ctx, ports.ErrUnavailable), nil
	}
	view, err := s.siteAccountStatsSvc.Read(ctx, GetActor(ctx), r.IndexerId)
	if err != nil {
		return siteAccountStatsReadProblem(ctx, err), nil
	}
	var observation *SiteAccountStatsObservation
	if view != nil {
		out := siteAccountStatsObservationResponse(*view)
		observation = &out
	}
	return ReadSiteAccountStats200JSONResponse{Observation: observation}, nil
}

func siteAccountStatsObservationResponse(v ports.SiteAccountStatsView) SiteAccountStatsObservation {
	out := SiteAccountStatsObservation{
		Config:              siteAccountStatsConfigResponse(v.Config),
		ObservedAt:          v.ObservedAt,
		SampleConfigVersion: v.SampleConfigVersion,
		SampleSourceVersion: v.SampleSourceVersion,
		LastAttemptAt:       v.LastAttemptAt,
		CollectionState:     SiteAccountStatsObservationCollectionState(v.CollectionState),
		Stale:               v.Stale,
	}
	if v.FailureCode != "" {
		code := SiteAccountStatsObservationFailureCode(v.FailureCode)
		out.FailureCode = &code
	}
	if v.Stats != nil {
		stats := v.Stats
		out.Stats = &SiteAccountStats{
			AccountId:  stats.AccountID,
			Uploaded:   siteAccountStatsAmountResponse(stats.Uploaded),
			Downloaded: siteAccountStatsAmountResponse(stats.Downloaded),
			Ratio:      SiteAccountStatsRatio{State: SiteAccountStatsRatioState(stats.Ratio.State)},
			Bonus:      stats.Bonus,
		}
		if stats.Ratio.Value != "" {
			value := stats.Ratio.Value
			out.Stats.Ratio.Value = &value
		}
	}
	return out
}

type siteAccountStatsReadErrorResponse struct{ problem ErrorResponse }

func siteAccountStatsReadProblem(ctx context.Context, err error) siteAccountStatsReadErrorResponse {
	status, code, message := 503, "SITE_ACCOUNT_STATS_UNAVAILABLE", "account statistics are temporarily unavailable"
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		status, code, message = 401, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "account statistics access denied"
	case errors.Is(err, ports.ErrRejected):
		status, code, message = 400, "INVALID_REQUEST", "invalid account statistics request"
	case errors.Is(err, ports.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "source not found"
	}
	return siteAccountStatsReadErrorResponse{problemResponse(status, code, message, GetRequestID(ctx))}
}

func (r siteAccountStatsReadErrorResponse) VisitReadSiteAccountStatsResponse(w http.ResponseWriter) error {
	writeProblem(w, r.problem.Status, r.problem)
	return nil
}
