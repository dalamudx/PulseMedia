package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/dalamudx/PulseMedia/internal/ports"
	calendarusecase "github.com/dalamudx/PulseMedia/internal/usecase/calendar"
)

type bangumiCalendarApplication interface {
	List(context.Context, int, string) (calendarusecase.Result, error)
}

func (s *ServerHandler) SetBangumiCalendarService(service bangumiCalendarApplication) {
	s.bangumiCalendarSvc = service
}
func (s *ServerHandler) GetBangumiCalendar(ctx context.Context, request GetBangumiCalendarRequestObject) (GetBangumiCalendarResponseObject, error) {
	actor, id := GetActor(ctx), GetRequestID(ctx)
	if actor == nil {
		return GetBangumiCalendar401ApplicationProblemPlusJSONResponse(problemResponse(http.StatusUnauthorized, "UNAUTHORIZED", "authentication required", id)), nil
	}
	if !metadataReadAllowed(actor) {
		return GetBangumiCalendar403ApplicationProblemPlusJSONResponse(problemResponse(http.StatusForbidden, "FORBIDDEN", "metadata read access denied", id)), nil
	}
	if s.bangumiCalendarSvc == nil {
		return GetBangumiCalendar503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "calendar temporarily unavailable", id)), nil
	}
	weekday, query := 0, ""
	if request.Params.Weekday != nil {
		weekday = *request.Params.Weekday
		if weekday < 1 || weekday > 7 {
			return GetBangumiCalendar400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_REQUEST", "invalid calendar filter", id)), nil
		}
	}
	if request.Params.Q != nil {
		query = *request.Params.Q
	}
	result, err := s.bangumiCalendarSvc.List(ctx, weekday, query)
	if errors.Is(err, ports.ErrRejected) {
		return GetBangumiCalendar400ApplicationProblemPlusJSONResponse(problemResponse(http.StatusBadRequest, "INVALID_REQUEST", "invalid calendar filter", id)), nil
	}
	if err != nil || result.Days == nil {
		return GetBangumiCalendar503ApplicationProblemPlusJSONResponse(problemResponse(http.StatusServiceUnavailable, "UNAVAILABLE", "calendar temporarily unavailable", id)), nil
	}
	out := BangumiCalendarResponse{Days: make([]BangumiCalendarDay, 0, len(result.Days)), ObservedAt: result.ObservedAt, ExecutionAllowed: false}
	for _, day := range result.Days {
		row := BangumiCalendarDay{Weekday: day.DayID, Name: day.DayName, Items: make([]BangumiCalendarItem, 0, len(day.Items))}
		for _, item := range day.Items {
			record := BangumiCalendarItem{BangumiId: strconv.FormatInt(item.ID, 10), Title: item.Title, OriginalTitle: item.OrigTitle, Rating: item.Rating}
			if item.AirDate != "" {
				value := item.AirDate
				record.FirstAirDate = &value
			}
			if item.PosterURL != "" {
				value := item.PosterURL
				record.PosterUrl = &value
			}
			row.Items = append(row.Items, record)
		}
		out.Days = append(out.Days, row)
	}
	return GetBangumiCalendar200JSONResponse(out), nil
}
