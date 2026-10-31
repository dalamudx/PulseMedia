package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	mediausecase "github.com/dalamudx/PulseMedia/internal/usecase/media"
	"net/http"
)

func (s *ServerHandler) SetFileMappingService(service *mediausecase.FileMappingService) {
	s.fileMappingSvc = service
}

type fileMappingFailure struct{ controlFailure }

func mappingFailure(ctx context.Context, e error) fileMappingFailure {
	f := failedControl(ctx, e)
	if errors.Is(e, ports.ErrInvalidFileMapping) {
		f.status = 400
		f.body.Code = "INVALID_FILE_MAPPING"
	}
	if errors.Is(e, ports.ErrFileMappingConflict) {
		f.status = 409
		f.body.Code = "FILE_MAPPING_CONFLICT"
	}
	f.body.Message = "file mapping request could not be completed"
	return fileMappingFailure{f}
}
func (e fileMappingFailure) VisitGetFileMappingOptionsResponse(w http.ResponseWriter) error {
	return e.VisitControlSubscriptionResponse(w)
}
func (e fileMappingFailure) VisitPreviewFileMappingResponse(w http.ResponseWriter) error {
	return e.VisitControlSubscriptionResponse(w)
}
func (e fileMappingFailure) VisitCorrectFileMappingResponse(w http.ResponseWriter) error {
	return e.VisitControlSubscriptionResponse(w)
}

type fileMappingSuccess struct{ body any }

func (s fileMappingSuccess) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(s.body)
}
func (s fileMappingSuccess) VisitGetFileMappingOptionsResponse(w http.ResponseWriter) error {
	return s.write(w)
}
func (s fileMappingSuccess) VisitPreviewFileMappingResponse(w http.ResponseWriter) error {
	return s.write(w)
}
func (s fileMappingSuccess) VisitCorrectFileMappingResponse(w http.ResponseWriter) error {
	return s.write(w)
}
func (s *ServerHandler) GetFileMappingOptions(ctx context.Context, r GetFileMappingOptionsRequestObject) (GetFileMappingOptionsResponseObject, error) {
	if s.fileMappingSvc == nil {
		return mappingFailure(ctx, ports.ErrUnavailable), nil
	}
	query := ports.FileMappingOptionsQuery{}
	if r.Params.RootId != nil {
		query.RootID = *r.Params.RootId
	}
	if r.Params.Cursor != nil {
		query.Cursor = *r.Params.Cursor
	}
	if r.Params.MediaQuery != nil {
		query.MediaQuery = *r.Params.MediaQuery
	}
	if r.Params.MediaType != nil {
		query.MediaType = string(*r.Params.MediaType)
	}
	if r.Params.TmdbId != nil {
		query.TMDBID = *r.Params.TmdbId
	}
	if r.Params.MediaCursor != nil {
		query.MediaCursor = *r.Params.MediaCursor
	}
	if r.Params.MediaId != nil {
		query.MediaID = *r.Params.MediaId
	}
	out, e := s.fileMappingSvc.Options(ctx, GetActor(ctx), query)
	if e != nil {
		return mappingFailure(ctx, e), nil
	}
	return fileMappingSuccess{out}, nil
}
func (s *ServerHandler) PreviewFileMapping(ctx context.Context, r PreviewFileMappingRequestObject) (PreviewFileMappingResponseObject, error) {
	if s.fileMappingSvc == nil {
		return mappingFailure(ctx, ports.ErrUnavailable), nil
	}
	out, e := s.fileMappingSvc.Preview(ctx, GetActor(ctx), r.Id, r.Params.MediaId)
	if e != nil {
		return mappingFailure(ctx, e), nil
	}
	return fileMappingSuccess{out}, nil
}
func (s *ServerHandler) CorrectFileMapping(ctx context.Context, r CorrectFileMappingRequestObject) (CorrectFileMappingResponseObject, error) {
	if s.fileMappingSvc == nil {
		return mappingFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return mappingFailure(ctx, ports.ErrInvalidFileMapping), nil
	}
	out, e := s.fileMappingSvc.Correct(ctx, GetActor(ctx), r.Id, ports.FileMappingCommand{MediaID: r.Body.MediaId, UnitIDs: r.Body.UnitIds, ExpectedStamp: r.Body.ExpectedStamp, ConfirmedCoverage: r.Body.ConfirmedCoverage}, r.Params.IdempotencyKey)
	if e != nil {
		return mappingFailure(ctx, e), nil
	}
	return fileMappingSuccess{out}, nil
}
