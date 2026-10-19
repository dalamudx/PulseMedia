package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (s *ServerHandler) GetLibraryOverview(ctx context.Context, r GetLibraryOverviewRequestObject) (GetLibraryOverviewResponseObject, error) {
	actor, id := GetActor(ctx), GetRequestID(ctx)
	if actor == nil {
		return GetLibraryOverview401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id)), nil
	}
	request := ports.LibraryOverviewRequest{}
	if r.Params.RecentLimit != nil {
		if *r.Params.RecentLimit < 1 || *r.Params.RecentLimit > 24 {
			return GetLibraryOverview400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_PAGE", "invalid recent import limit", id)), nil
		}
		request.RecentLimit = *r.Params.RecentLimit
	}
	if s.libraryOverviewSvc == nil {
		return GetLibraryOverview503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "library temporarily unavailable", id)), nil
	}
	page, err := s.libraryOverviewSvc.Read(ctx, actor, request)
	if errors.Is(err, ports.ErrForbidden) {
		return GetLibraryOverview403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "library access denied", id)), nil
	}
	if errors.Is(err, ports.ErrInvalidLibraryQuery) {
		return GetLibraryOverview400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_PAGE", "invalid recent import limit", id)), nil
	}
	if err != nil || page == nil {
		return GetLibraryOverview503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "library temporarily unavailable", id)), nil
	}
	out := LibraryOverview{Counts: LibraryOverviewCounts{Movies: page.Counts.Movies, TvShows: page.Counts.TVShows, Episodes: page.Counts.Episodes, Files: page.Counts.Files}, Roots: []LibraryRootOverview{}, Recent: []LibraryRecentImport{}, ObservedAt: page.ObservedAt}
	for _, root := range page.Roots {
		out.Roots = append(out.Roots, LibraryRootOverview{RootId: root.RootID, Name: root.Name, MediaType: LibraryRootOverviewMediaType(root.MediaType), Health: root.Health, Availability: LibraryRootOverviewAvailability(root.Availability), CapacityState: LibraryRootOverviewCapacityState(root.CapacityState), TotalBytes: root.TotalBytes, AvailableBytes: root.AvailableBytes, SampledAt: root.SampledAt})
	}
	for _, item := range page.Recent {
		var year *int
		if item.Year != nil {
			value := int(*item.Year)
			year = &value
		}
		out.Recent = append(out.Recent, LibraryRecentImport{MediaId: item.MediaID, MediaType: LibraryRecentImportMediaType(item.MediaType), Title: item.Title, Year: year, ImportedAt: item.ImportedAt, PosterUrl: item.PosterURL})
	}
	return GetLibraryOverview200JSONResponse(out), nil
}

func (s *ServerHandler) GetDashboardActivity(ctx context.Context, r GetDashboardActivityRequestObject) (GetDashboardActivityResponseObject, error) {
	actor, id := GetActor(ctx), GetRequestID(ctx)
	if actor == nil {
		return GetDashboardActivity401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id)), nil
	}
	limit := 0
	if r.Params.Limit != nil {
		if *r.Params.Limit < 1 || *r.Params.Limit > 12 {
			return GetDashboardActivity400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_PAGE", "invalid activity limit", id)), nil
		}
		limit = *r.Params.Limit
	}
	if s.dashboardActivitySvc == nil {
		return GetDashboardActivity503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "activity temporarily unavailable", id)), nil
	}
	page, err := s.dashboardActivitySvc.Read(ctx, actor, limit)
	if errors.Is(err, ports.ErrForbidden) {
		return GetDashboardActivity403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "activity access denied", id)), nil
	}
	if errors.Is(err, ports.ErrInvalidOperationQuery) {
		return GetDashboardActivity400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_PAGE", "invalid activity limit", id)), nil
	}
	if err != nil || page == nil {
		return GetDashboardActivity503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "activity temporarily unavailable", id)), nil
	}
	out := DashboardActivityPage{Items: []DashboardActivity{}, More: page.More, ObservedAt: page.ObservedAt}
	for _, item := range page.Items {
		value := DashboardActivity{OperationId: item.OperationID, Title: item.Title, Status: item.Status, Progress: item.Progress, UpdatedAt: item.UpdatedAt}
		if item.ProgressSource != "" {
			source := DashboardActivityProgressSource(item.ProgressSource)
			value.ProgressSource = &source
		}
		out.Items = append(out.Items, value)
	}
	return GetDashboardActivity200JSONResponse(out), nil
}
