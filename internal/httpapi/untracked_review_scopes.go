package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type untrackedReviewScopeApplication interface {
	ScopeOptions(context.Context, *ports.Actor, string) (ports.UntrackedReviewScopePage, error)
}

// The scope list is read-only and rejects HEAD/default method fallthrough.
func validUntrackedReviewScopesTransport(r *http.Request) bool {
	if r == nil || r.URL.Path != untrackedScopePrefix || r.Method != http.MethodGet || r.URL.ForceQuery || len(r.Header.Values("Idempotency-Key")) != 0 {
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) > 1 {
		return false
	}
	if len(query) > 0 && (len(query["cursor"]) != 1 || !canonicalUntrackedReviewID(query.Get("cursor"))) {
		return false
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

func (s *ServerHandler) ListUntrackedReviewScopes(ctx context.Context, r ListUntrackedReviewScopesRequestObject) (ListUntrackedReviewScopesResponseObject, error) {
	actor := GetActor(ctx)
	if err := untrackedScopeActorError(actor); err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	cursor := ""
	if r.Params.Cursor != nil {
		cursor = *r.Params.Cursor
		if !canonicalUntrackedReviewID(cursor) {
			return untrackedReviewProblem(ctx, ports.ErrRejected), nil
		}
	}
	service, ok := s.untrackedReviewSvc.(untrackedReviewScopeApplication)
	if !ok {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	page, err := service.ScopeOptions(ctx, actor, cursor)
	if err != nil {
		return untrackedReviewProblem(ctx, err), nil
	}
	if page.Items == nil || len(page.Items) > 100 || page.HasMore != (page.NextCursor != "") {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	items := make([]UntrackedReviewScopeOption, 0, len(page.Items))
	previous := cursor
	for _, o := range page.Items {
		if !canonicalUntrackedReviewID(o.ID) || o.ID <= previous || o.Version < 1 || o.LocalRoot == "" || o.TargetRootPath == "" || (o.TargetMediaType != "movie" && o.TargetMediaType != "tv") {
			return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
		}
		previous = o.ID
		items = append(items, UntrackedReviewScopeOption{Id: o.ID, Version: o.Version, Enabled: o.Enabled, LocalRoot: o.LocalRoot, TargetRootPath: o.TargetRootPath, TargetMediaType: o.TargetMediaType})
	}
	if page.HasMore && (len(items) != 100 || page.NextCursor != previous) {
		return untrackedReviewProblem(ctx, ports.ErrUnavailable), nil
	}
	return ListUntrackedReviewScopes200JSONResponse{Items: items, NextCursor: page.NextCursor, HasMore: page.HasMore}, nil
}

func (r untrackedScopeErrorResponse) VisitListUntrackedReviewScopesResponse(w http.ResponseWriter) error {
	return r.write(w)
}
