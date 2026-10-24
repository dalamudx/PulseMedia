package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type untrackedDiscoveryApplication interface {
	Read(context.Context, *ports.Actor, uuid.UUID, string, int) (ports.UntrackedDiscoveryPage, error)
}

func (s *ServerHandler) SetUntrackedDiscoveryService(service untrackedDiscoveryApplication) {
	s.untrackedDiscoverySvc = service
}

func validUntrackedDiscoveryTransport(r *http.Request, id string) bool {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.String() != id || r.Method != http.MethodGet || len(r.Header.Values("Idempotency-Key")) != 0 || r.URL.ForceQuery {
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range query {
		if len(values) != 1 {
			return false
		}
		switch key {
		case "cursor":
			if len(values[0]) > 2048 {
				return false
			}
		case "limit":
			n, e := strconv.Atoi(values[0])
			if e != nil || n < 1 || n > 64 {
				return false
			}
		default:
			return false
		}
	}
	if r.Body != nil {
		defer func() { _ = r.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(body) != 0 {
			return false
		}
	}
	return true
}
func (s *ServerHandler) ListUntrackedDiscoveries(ctx context.Context, r ListUntrackedDiscoveriesRequestObject) (ListUntrackedDiscoveriesResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedScopeProblem(ctx, err), nil
	}
	if r.ScopeId == uuid.Nil {
		return untrackedScopeProblem(ctx, ports.ErrRejected), nil
	}
	limit, cursor := 64, ""
	if r.Params.Limit != nil {
		limit = *r.Params.Limit
	}
	if r.Params.Cursor != nil {
		cursor = *r.Params.Cursor
	}
	if limit < 1 || limit > 64 || len(cursor) > 2048 {
		return untrackedScopeProblem(ctx, ports.ErrRejected), nil
	}
	if s.untrackedDiscoverySvc == nil {
		return untrackedScopeProblem(ctx, ports.ErrUnavailable), nil
	}
	page, err := s.untrackedDiscoverySvc.Read(ctx, actor, r.ScopeId, cursor, limit)
	if err != nil {
		return untrackedScopeProblem(ctx, err), nil
	}
	items := make([]UntrackedDiscoveryObservation, 0, len(page.Files))
	for _, file := range page.Files {
		items = append(items, UntrackedDiscoveryObservation{Id: file.ID, ScopeId: file.ScopeID, ScopeVersion: file.ScopeVersion, Revision: file.Revision, RelativePath: file.RelativePath, Device: file.Identity.Device, Inode: file.Identity.Inode, Size: file.Identity.Size, Mtime: file.Identity.Mtime, FirstObservedAt: file.FirstObservedAt, LastObservedAt: file.LastObservedAt, Observations: file.Observations})
	}
	return ListUntrackedDiscoveries200JSONResponse{Items: items, NextCursor: page.NextCursor, HasMore: !page.Complete, InventoryComplete: false, ExecutionAllowed: false}, nil
}
func (r untrackedScopeErrorResponse) VisitListUntrackedDiscoveriesResponse(w http.ResponseWriter) error {
	return r.write(w)
}
