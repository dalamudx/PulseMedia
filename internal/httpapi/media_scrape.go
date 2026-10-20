package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/mediascrape"
)

// SetMediaScrapeService injects the library-wide sidecar gap filling use case.
func (s *ServerHandler) SetMediaScrapeService(service *mediascrape.Service) {
	s.mediaScrapeSvc = service
}

func mediaScrapePreview(preview ports.MediaScrapePreview) MediaScrapePreview {
	return MediaScrapePreview{
		Total: preview.Total, Missing: preview.Missing, Queued: preview.Queued,
		Running: preview.Running, Complete: preview.Complete, Review: preview.Review,
	}
}

func mediaScrapeBatch(progress ports.MediaScrapeProgress) MediaScrapeBatch {
	return MediaScrapeBatch{
		Id: progress.ID, CreatedAt: progress.CreatedAt, Total: progress.Total,
		Queued: progress.Queued, Running: progress.Running,
		Succeeded: progress.Succeeded, Review: progress.Review,
	}
}

func mediaScrapeFailure(ctx context.Context, err error) (ReadMediaScrapeResponseObject, error) {
	requestID := GetRequestID(ctx)
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		return ReadMediaScrape401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", requestID)), nil
	case errors.Is(err, ports.ErrForbidden):
		return ReadMediaScrape403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "administrator access required", requestID)), nil
	case errors.Is(err, ports.ErrRejected):
		return ReadMediaScrape400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_REQUEST", "invalid command key", requestID)), nil
	default:
		return ReadMediaScrape503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "scrape state unavailable", requestID)), nil
	}
}

// ReadMediaScrape reports the gap-filling preview and the latest batch.
func (s *ServerHandler) ReadMediaScrape(ctx context.Context, request ReadMediaScrapeRequestObject) (ReadMediaScrapeResponseObject, error) {
	if s.mediaScrapeSvc == nil {
		return ReadMediaScrape503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "scrape service unavailable", GetRequestID(ctx))), nil
	}
	preview, err := s.mediaScrapeSvc.Preview(ctx, GetActor(ctx))
	if err != nil {
		return mediaScrapeFailure(ctx, err)
	}
	key := ""
	if request.Params.CommandKey != nil {
		key = *request.Params.CommandKey
	}
	progress, err := s.mediaScrapeSvc.Status(ctx, GetActor(ctx), key)
	if err != nil {
		return mediaScrapeFailure(ctx, err)
	}
	status := MediaScrapeStatus{Preview: mediaScrapePreview(preview)}
	if progress != nil {
		batch := mediaScrapeBatch(*progress)
		status.Batch = &batch
	}
	return ReadMediaScrape200JSONResponse(status), nil
}

// StartMediaScrape queues one batch for the current administrator session. The
// Idempotency-Key names the command: repeating it returns the original batch.
func (s *ServerHandler) StartMediaScrape(ctx context.Context, request StartMediaScrapeRequestObject) (StartMediaScrapeResponseObject, error) {
	requestID := GetRequestID(ctx)
	if s.mediaScrapeSvc == nil {
		return StartMediaScrape503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "scrape service unavailable", requestID)), nil
	}
	if request.Body == nil || !request.Body.All {
		return StartMediaScrape400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_REQUEST", "all must be true", requestID)), nil
	}
	progress, _, err := s.mediaScrapeSvc.Start(ctx, GetActor(ctx), request.Params.IdempotencyKey)
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrUnauthorized):
			return StartMediaScrape401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", requestID)), nil
		case errors.Is(err, ports.ErrForbidden):
			return StartMediaScrape403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "administrator access required", requestID)), nil
		case errors.Is(err, ports.ErrRejected):
			return StartMediaScrape400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "NOTHING_TO_QUEUE", "no file needs sidecar work", requestID)), nil
		default:
			return StartMediaScrape503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "scrape state unavailable", requestID)), nil
		}
	}
	return StartMediaScrape200JSONResponse(mediaScrapeBatch(progress)), nil
}
