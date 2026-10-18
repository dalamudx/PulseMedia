package httpapi

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (s *ServerHandler) ListLibraryCatalog(ctx context.Context, r ListLibraryCatalogRequestObject) (ListLibraryCatalogResponseObject, error) {
	a := GetActor(ctx)
	id := GetRequestID(ctx)
	if a == nil {
		return ListLibraryCatalog401ApplicationProblemPlusJSONResponse(problemResponse(401, "UNAUTHORIZED", "authentication required", id)), nil
	}
	request := ports.LibraryCatalogRequest{}
	if r.Params.Limit != nil {
		if *r.Params.Limit < 1 || *r.Params.Limit > 100 {
			return ListLibraryCatalog400ApplicationProblemPlusJSONResponse(problemResponse(400, "INVALID_PAGE", "invalid library page", id)), nil
		}
		request.Limit = *r.Params.Limit
	}
	if r.Params.Cursor != nil {
		request.Cursor = *r.Params.Cursor
	}
	if s.libraryCatalogSvc == nil {
		return ListLibraryCatalog503ApplicationProblemPlusJSONResponse(problemResponse(503, "UNAVAILABLE", "library temporarily unavailable", id)), nil
	}
	page, e := s.libraryCatalogSvc.List(ctx, a, request)
	if errors.Is(e, ports.ErrForbidden) {
		return ListLibraryCatalog403ApplicationProblemPlusJSONResponse(problemResponse(403, "FORBIDDEN", "library access denied", id)), nil
	}
	if errors.Is(e, ports.ErrInvalidLibraryQuery) {
		return ListLibraryCatalog400ApplicationProblemPlusJSONResponse(problemResponse(400, "INVALID_PAGE", "invalid library page", id)), nil
	}
	if e != nil {
		return ListLibraryCatalog503ApplicationProblemPlusJSONResponse(problemResponse(503, "UNAVAILABLE", "library temporarily unavailable", id)), nil
	}
	out := LibraryCatalogPage{Items: []LibraryCatalogEntry{}}
	for _, v := range page.Items {
		var year *int
		if v.Year != nil {
			value := int(*v.Year)
			year = &value
		}
		out.Items = append(out.Items, LibraryCatalogEntry{FileId: v.FileID, UnitId: v.UnitID, MediaId: v.MediaID, RootId: v.RootID, Title: v.Title, Year: year, MediaType: LibraryCatalogEntryMediaType(v.MediaType), UnitKey: v.UnitKey, RootName: v.RootName, RootHealth: v.RootHealth, RelativePath: v.RelativePath, SizeBytes: v.SizeBytes, Imported: v.Imported, Confirmed: v.Confirmed})
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	return ListLibraryCatalog200JSONResponse(out), nil
}
