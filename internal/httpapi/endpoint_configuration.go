package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
)

func (s *ServerHandler) endpointConfig(ctx context.Context) (*settings.EndpointConfigurationService, *ports.Actor, error) {
	actor := GetActor(ctx)
	if actor == nil {
		return nil, nil, ports.ErrUnauthorized
	}
	if s.endpointConfigurationSvc == nil {
		return nil, actor, ports.ErrUnavailable
	}
	return s.endpointConfigurationSvc, actor, nil
}

func endpointDownloadDTO(v ports.EndpointConfiguration) DownloadClientDetails {
	return DownloadClientDetails{Id: v.ID, Name: v.Name, ClientType: v.Protocol, Url: v.URL, Username: v.Username, UpdatedAt: v.UpdatedAt}
}

func endpointMediaDTO(v ports.EndpointConfiguration) MediaServerDetails {
	return MediaServerDetails{Id: v.ID, Name: v.Name, ServerType: v.Protocol, Url: v.URL, Health: v.Health, UpdatedAt: v.UpdatedAt}
}

func endpointCreateMapping(v *map[string]string) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(*v)
	if err != nil {
		return nil, ports.ErrInvalidEndpointConfiguration
	}
	return data, nil
}

func (s *ServerHandler) ListDownloadClients(ctx context.Context, _ ListDownloadClientsRequestObject) (ListDownloadClientsResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	rows, err := svc.List(ctx, actor, ports.DownloadClientEndpoint)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	items := make([]DownloadClientDetails, 0, len(rows))
	for _, row := range rows {
		items = append(items, endpointDownloadDTO(row))
	}
	return ListDownloadClients200JSONResponse{Items: items}, nil
}

func (s *ServerHandler) ListMediaServers(ctx context.Context, _ ListMediaServersRequestObject) (ListMediaServersResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	rows, err := svc.List(ctx, actor, ports.MediaServerEndpoint)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	items := make([]MediaServerDetails, 0, len(rows))
	for _, row := range rows {
		items = append(items, endpointMediaDTO(row))
	}
	return ListMediaServers200JSONResponse{Items: items}, nil
}

func (s *ServerHandler) CreateDownloadClient(ctx context.Context, request CreateDownloadClientRequestObject) (CreateDownloadClientResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if request.Body == nil {
		return endpointConfigurationError(ctx, ports.ErrInvalidEndpointConfiguration), nil
	}
	body := request.Body
	mapping, err := endpointCreateMapping(body.RemotePathMap)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	row, err := svc.Create(ctx, actor, ports.DownloadClientEndpoint, ports.EndpointConfigurationInput{Name: body.Name, Protocol: string(body.ClientType), URL: body.Url, Username: body.Username, Secret: body.Password, Mapping: mapping})
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if row == nil {
		return endpointConfigurationError(ctx, ports.ErrUnavailable), nil
	}
	return CreateDownloadClient201JSONResponse(endpointDownloadDTO(*row)), nil
}

func (s *ServerHandler) CreateMediaServer(ctx context.Context, request CreateMediaServerRequestObject) (CreateMediaServerResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if request.Body == nil {
		return endpointConfigurationError(ctx, ports.ErrInvalidEndpointConfiguration), nil
	}
	body := request.Body
	mapping, err := endpointCreateMapping(body.PathMapping)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	row, err := svc.Create(ctx, actor, ports.MediaServerEndpoint, ports.EndpointConfigurationInput{Name: body.Name, Protocol: string(body.ServerType), URL: body.Url, Secret: body.ApiKey, Mapping: mapping})
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if row == nil {
		return endpointConfigurationError(ctx, ports.ErrUnavailable), nil
	}
	return CreateMediaServer201JSONResponse(endpointMediaDTO(*row)), nil
}

func (s *ServerHandler) UpdateDownloadClient(ctx context.Context, request UpdateDownloadClientRequestObject) (UpdateDownloadClientResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if request.Body == nil {
		return endpointConfigurationError(ctx, ports.ErrInvalidEndpointConfiguration), nil
	}
	body := request.Body
	row, err := svc.Update(ctx, actor, ports.DownloadClientEndpoint, request.Id, ports.EndpointConfigurationInput{Name: body.Name, Protocol: string(body.ClientType), URL: body.Url, Username: body.Username, Secret: body.Password, ExpectedUpdatedAt: body.ExpectedUpdatedAt})
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if row == nil {
		return endpointConfigurationError(ctx, ports.ErrUnavailable), nil
	}
	return UpdateDownloadClient200JSONResponse(endpointDownloadDTO(*row)), nil
}

func (s *ServerHandler) UpdateMediaServer(ctx context.Context, request UpdateMediaServerRequestObject) (UpdateMediaServerResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if request.Body == nil {
		return endpointConfigurationError(ctx, ports.ErrInvalidEndpointConfiguration), nil
	}
	body := request.Body
	row, err := svc.Update(ctx, actor, ports.MediaServerEndpoint, request.Id, ports.EndpointConfigurationInput{Name: body.Name, Protocol: string(body.ServerType), URL: body.Url, Secret: body.ApiKey, ExpectedUpdatedAt: body.ExpectedUpdatedAt})
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if row == nil {
		return endpointConfigurationError(ctx, ports.ErrUnavailable), nil
	}
	return UpdateMediaServer200JSONResponse(endpointMediaDTO(*row)), nil
}

func (s *ServerHandler) DeleteDownloadClient(ctx context.Context, request DeleteDownloadClientRequestObject) (DeleteDownloadClientResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if err = svc.Delete(ctx, actor, ports.DownloadClientEndpoint, request.Id); err != nil {
		response := endpointConfigurationError(ctx, err)
		if errors.Is(err, ports.ErrEndpointInUse) {
			response.code = "CLIENT_IN_USE"
		}
		return response, nil
	}
	return DeleteDownloadClient204Response{}, nil
}

func (s *ServerHandler) DeleteMediaServer(ctx context.Context, request DeleteMediaServerRequestObject) (DeleteMediaServerResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if err = svc.Delete(ctx, actor, ports.MediaServerEndpoint, request.Id); err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	return DeleteMediaServer204Response{}, nil
}

func (s *ServerHandler) TestDownloadClient(ctx context.Context, request TestDownloadClientRequestObject) (TestDownloadClientResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	result, err := svc.Probe(ctx, actor, ports.DownloadClientEndpoint, request.Id)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if result == nil {
		return endpointConfigurationError(ctx, ports.ErrUnavailable), nil
	}
	return TestDownloadClient200JSONResponse{Alive: result.Alive, Message: result.Message}, nil
}

func (s *ServerHandler) TestMediaServer(ctx context.Context, request TestMediaServerRequestObject) (TestMediaServerResponseObject, error) {
	svc, actor, err := s.endpointConfig(ctx)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	result, err := svc.Probe(ctx, actor, ports.MediaServerEndpoint, request.Id)
	if err != nil {
		return endpointConfigurationError(ctx, err), nil
	}
	if result == nil {
		return endpointConfigurationError(ctx, ports.ErrUnavailable), nil
	}
	return TestMediaServer200JSONResponse{Alive: result.Alive, Message: result.Message}, nil
}

// One safe renderer serves all configuration operations; no driver or network
// error text can become a public response.
type endpointConfigurationHTTPError struct {
	status                   int
	code, message, requestID string
}

func endpointConfigurationError(ctx context.Context, err error) endpointConfigurationHTTPError {
	r := endpointConfigurationHTTPError{status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", message: "endpoint configuration unavailable", requestID: GetRequestID(ctx)}
	switch {
	case errors.Is(err, ports.ErrUnauthorized):
		r.status, r.code, r.message = http.StatusUnauthorized, "UNAUTHORIZED", "authentication required"
	case errors.Is(err, ports.ErrForbidden):
		r.status, r.code, r.message = http.StatusForbidden, "FORBIDDEN", "current account is not permitted"
	case errors.Is(err, ports.ErrInvalidEndpointConfiguration):
		r.status, r.code, r.message = http.StatusBadRequest, "INVALID_ENDPOINT_CONFIGURATION", "invalid endpoint configuration"
	case errors.Is(err, ports.ErrNotFound):
		r.status, r.code, r.message = http.StatusNotFound, "NOT_FOUND", "endpoint not found"
	case errors.Is(err, ports.ErrEndpointVersionConflict):
		r.status, r.code, r.message = http.StatusConflict, "ENDPOINT_VERSION_CONFLICT", "endpoint changed; reload before editing"
	case errors.Is(err, ports.ErrEndpointInUse):
		r.status, r.code, r.message = http.StatusConflict, "ENDPOINT_IN_USE", "endpoint is referenced; resolve references before removal"
	}
	return r
}

func (r endpointConfigurationHTTPError) write(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "no-store")
	WriteJSONError(w, r.status, r.code, r.message, r.requestID)
	return nil
}

func (r endpointConfigurationHTTPError) VisitListDownloadClientsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r endpointConfigurationHTTPError) VisitCreateDownloadClientResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r endpointConfigurationHTTPError) VisitUpdateDownloadClientResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r endpointConfigurationHTTPError) VisitDeleteDownloadClientResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r endpointConfigurationHTTPError) VisitTestDownloadClientResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r endpointConfigurationHTTPError) VisitListMediaServersResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r endpointConfigurationHTTPError) VisitCreateMediaServerResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r endpointConfigurationHTTPError) VisitUpdateMediaServerResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r endpointConfigurationHTTPError) VisitDeleteMediaServerResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r endpointConfigurationHTTPError) VisitTestMediaServerResponse(w http.ResponseWriter) error {
	return r.write(w)
}
