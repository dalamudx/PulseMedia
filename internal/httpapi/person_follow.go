package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/personfollow"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func (s *ServerHandler) SetPersonFollowService(service *personfollow.ConfigService) {
	s.personFollowSvc = service
}
func personFollowActorError(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, err := uuid.Parse(a.ID)
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || (a.Role != ports.RoleAdmin && a.Role != ports.RoleMember) || err != nil || id == uuid.Nil || id.String() != a.ID {
		return ports.ErrForbidden
	}
	return nil
}
func (s *ServerHandler) personFollowReady(ctx context.Context) error {
	if err := personFollowActorError(GetActor(ctx)); err != nil {
		return err
	}
	if s.personFollowSvc == nil {
		return ports.ErrUnavailable
	}
	return nil
}

type personFollowFailure struct{ controlFailure }

func (r personFollowFailure) VisitListPersonFollowsResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func (r personFollowFailure) VisitGetPersonFollowResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func (r personFollowFailure) VisitSavePersonFollowResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func (r personFollowFailure) VisitGetPersonFollowReceiptResponse(w http.ResponseWriter) error {
	return r.VisitControlSubscriptionResponse(w)
}
func failedPersonFollow(ctx context.Context, err error) personFollowFailure {
	f := failedControl(ctx, err)
	if errors.Is(err, ports.ErrRejected) {
		f.status = http.StatusBadRequest
		f.body.Code = "INVALID_REQUEST"
	}
	if errors.Is(err, ports.ErrPersonFollowConflict) {
		f.status = http.StatusConflict
		f.body.Code = "CONFLICT"
	}
	f.body.Message = "person follow request could not be completed"
	return personFollowFailure{f}
}
func personFollowDomainSettings(in PersonFollowSettings) domain.Settings {
	settings := domain.Settings{PersonID: in.PersonId, Role: string(in.Role), Enabled: in.Enabled, MinReleaseDate: in.MinReleaseDate.Format("2006-01-02"), IntervalMinutes: in.IntervalMinutes, MovieRootID: in.MovieRootId, TVRootID: in.TvRootId, QualityPolicyID: in.QualityPolicyId, QualityPolicyRevision: in.QualityPolicyRevision}
	if in.Bindings != nil {
		settings.Bindings = &domain.Bindings{IndexerIDs: in.Bindings.IndexerIds, DownloaderID: in.Bindings.DownloaderId}
	}
	return settings
}
func mapPersonFollowConfig(in ports.PersonFollowConfig) PersonFollowConfiguration {
	settings := in.Settings
	date, _ := time.Parse("2006-01-02", settings.MinReleaseDate)
	mapped := PersonFollowSettings{PersonId: settings.PersonID, Role: PersonFollowSettingsRole(settings.Role), Enabled: settings.Enabled, MinReleaseDate: openapi_types.Date{Time: date}, IntervalMinutes: settings.IntervalMinutes, MovieRootId: settings.MovieRootID, TvRootId: settings.TVRootID, QualityPolicyId: settings.QualityPolicyID, QualityPolicyRevision: settings.QualityPolicyRevision}
	if settings.Bindings != nil {
		mapped.Bindings = &PersonFollowBindings{IndexerIds: append([]uuid.UUID{}, settings.Bindings.IndexerIDs...), DownloaderId: settings.Bindings.DownloaderID}
	}
	return PersonFollowConfiguration{Id: in.ID, OwnerId: in.OwnerID, Settings: mapped, Version: in.Version, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt}
}
func mapPersonFollowReceipt(in *ports.PersonFollowReceipt) PersonFollowReceipt {
	return PersonFollowReceipt{CommandKey: in.CommandKey, Config: mapPersonFollowConfig(in.Config)}
}
func (s *ServerHandler) ListPersonFollows(ctx context.Context, _ ListPersonFollowsRequestObject) (ListPersonFollowsResponseObject, error) {
	if err := s.personFollowReady(ctx); err != nil {
		return failedPersonFollow(ctx, err), nil
	}
	rows, err := s.personFollowSvc.List(ctx, GetActor(ctx))
	if err != nil {
		return failedPersonFollow(ctx, err), nil
	}
	result := make(ListPersonFollows200JSONResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, mapPersonFollowConfig(row))
	}
	return result, nil
}
func (s *ServerHandler) GetPersonFollow(ctx context.Context, r GetPersonFollowRequestObject) (GetPersonFollowResponseObject, error) {
	if err := s.personFollowReady(ctx); err != nil {
		return failedPersonFollow(ctx, err), nil
	}
	result, err := s.personFollowSvc.Read(ctx, GetActor(ctx), r.Id)
	if err != nil {
		return failedPersonFollow(ctx, err), nil
	}
	return GetPersonFollow200JSONResponse(mapPersonFollowConfig(*result)), nil
}
func (s *ServerHandler) SavePersonFollow(ctx context.Context, r SavePersonFollowRequestObject) (SavePersonFollowResponseObject, error) {
	if err := s.personFollowReady(ctx); err != nil {
		return failedPersonFollow(ctx, err), nil
	}
	if r.Body == nil {
		return failedPersonFollow(ctx, ports.ErrRejected), nil
	}
	input := ports.PersonFollowInput{ExpectedVersion: r.Body.ExpectedVersion, Settings: personFollowDomainSettings(r.Body.Settings)}
	result, err := s.personFollowSvc.Save(ctx, GetActor(ctx), r.Id, input, r.Params.IdempotencyKey)
	if err != nil {
		return failedPersonFollow(ctx, err), nil
	}
	return SavePersonFollow200JSONResponse(mapPersonFollowReceipt(result)), nil
}
func (s *ServerHandler) GetPersonFollowReceipt(ctx context.Context, r GetPersonFollowReceiptRequestObject) (GetPersonFollowReceiptResponseObject, error) {
	if err := s.personFollowReady(ctx); err != nil {
		return failedPersonFollow(ctx, err), nil
	}
	result, err := s.personFollowSvc.Receipt(ctx, GetActor(ctx), r.Key)
	if err != nil {
		return failedPersonFollow(ctx, err), nil
	}
	return GetPersonFollowReceipt200JSONResponse(mapPersonFollowReceipt(result)), nil
}
