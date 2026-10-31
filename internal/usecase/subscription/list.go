package subscription

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type ListService struct{ reader ports.SubscriptionListReader }

func NewListService(reader ports.SubscriptionListReader) *ListService {
	return &ListService{reader: reader}
}

func (s *ListService) List(ctx context.Context, actor *ports.Actor, in ports.SubscriptionListQuery) (*ports.SubscriptionListPage, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	if s.reader == nil {
		return nil, ports.ErrUnavailable
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	in.Search = strings.TrimSpace(in.Search)
	if in.Limit < 1 || in.Limit > 100 || in.Offset < 0 || in.Offset > 1000000 || utf8.RuneCountInString(in.Search) > 200 || (in.MediaType != "" && in.MediaType != "movie" && in.MediaType != "tv") {
		return nil, ports.ErrInvalidSubscriptionQuery
	}
	if in.Calendar && (in.Start.IsZero() || in.End.IsZero() || in.End.Before(in.Start) || in.End.Sub(in.Start) > 41*24*time.Hour || in.Search != "" || in.TMDBID != "" || in.OwnOnly || in.MediaType != "") {
		return nil, ports.ErrInvalidSubscriptionQuery
	}
	if !in.Calendar && (in.UnknownDates || !in.Start.IsZero() || !in.End.IsZero()) {
		return nil, ports.ErrInvalidSubscriptionQuery
	}
	return s.reader.ReadSubscriptionList(ctx, actor, in)
}
