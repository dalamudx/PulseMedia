package httpapi

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	errEndpointRevision = errors.New("endpoint revision changed or missing")
	errEndpointMasked   = errors.New("endpoint URL contains a display placeholder")
)

// The expected revision and raw URL are read under the same row lock as the
// update. Equal projections alone cannot distinguish two different secrets.
func resolveEndpointEditURL(submitted, current string, hasVersion, versionMatches bool) (string, error) {
	if hasVersion && !versionMatches {
		return "", errEndpointRevision
	}
	display := endpointDisplayURL(current)
	if submitted == display && display != current {
		if !hasVersion {
			return "", errEndpointRevision
		}
		return current, nil
	}
	if endpointURLHasMask(submitted) {
		return "", errEndpointMasked
	}
	return submitted, nil
}

func endpointURLHasMask(raw string) bool {
	decoded, err := url.QueryUnescape(raw)
	return err != nil || strings.Contains(decoded, "***")
}

func (s *ServerHandler) beginEndpointEdit(ctx context.Context) (pgx.Tx, *db.Queries, error) {
	actor := GetActor(ctx)
	if actor == nil || !actor.IsAdmin() || actor.Scopes != nil {
		return nil, nil, ports.ErrForbidden
	}
	userID, err := uuid.Parse(actor.ID)
	if err != nil {
		return nil, nil, ports.ErrForbidden
	}
	tx, err := s.app.DBPool().Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	q := s.app.Queries().WithTx(tx)
	user, err := q.LockSettingsUser(ctx, userID)
	if err != nil || !user.IsActive || user.Role != "admin" {
		_ = tx.Rollback(ctx)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
		return nil, nil, ports.ErrForbidden
	}
	return tx, q, nil
}
