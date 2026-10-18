package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (s *ServerHandler) GetSettingCommand(ctx context.Context, r GetSettingCommandRequestObject) (GetSettingCommandResponseObject, error) {
	id := GetRequestID(ctx)
	a := GetActor(ctx)
	if a == nil || a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil {
		return GetSettingCommand403JSONResponse{Code: "FORBIDDEN", Message: "current administrator session required", RequestId: id}, nil
	}
	if s.settingsSvc == nil {
		return GetSettingCommand503JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "settings temporarily unavailable", RequestId: id}, nil
	}
	result, e := s.settingsSvc.ReadSettingReceipt(ctx, a, r.Key, r.RequestKey)
	if e != nil {
		switch {
		case errors.Is(e, ports.ErrForbidden), errors.Is(e, ports.ErrUnauthorized):
			return GetSettingCommand403JSONResponse{Code: "FORBIDDEN", Message: "current administrator session required", RequestId: id}, nil
		case errors.Is(e, ports.ErrNotFound):
			return GetSettingCommand404JSONResponse{Code: "RECEIPT_UNKNOWN", Message: "original request not yet confirmed", RequestId: id}, nil
		case errors.Is(e, ports.ErrCommandConflict):
			return GetSettingCommand409JSONResponse{Code: "IDEMPOTENCY_CONFLICT", Message: "request key belongs to another command", RequestId: id}, nil
		case errors.Is(e, ports.ErrInvalidSettingCommand):
			return GetSettingCommand400JSONResponse{Code: "INVALID_COMMAND", Message: "invalid request key", RequestId: id}, nil
		default:
			return GetSettingCommand503JSONResponse{Code: "SERVICE_UNAVAILABLE", Message: "receipt read temporarily unavailable", RequestId: id}, nil
		}
	}
	raw, e := json.Marshal(result.Data.Value)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	var value map[string]any
	if e = json.Unmarshal(raw, &value); e != nil {
		return nil, ports.ErrUnavailable
	}
	return GetSettingCommand200JSONResponse{Key: result.Data.Key, Value: value, Version: int(result.Data.Version), UpdatedAt: result.Data.UpdatedAt}, nil
}
