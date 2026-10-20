package site

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
	"sync"
	"testing"
	"time"
)

type historyRepoFunc func(context.Context, *ports.Actor, uuid.UUID, int32, *ports.SiteCheckinHistoryCursor) (*ports.SiteCheckinHistoryPage, error)

func (f historyRepoFunc) ReadSiteCheckinHistory(c context.Context, a *ports.Actor, id uuid.UUID, l int32, p *ports.SiteCheckinHistoryCursor) (*ports.SiteCheckinHistoryPage, error) {
	return f(c, a, id, l, p)
}
func historyActor() *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
}
func historyRow(id uuid.UUID) ports.SiteCheckinDayView {
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	return ports.SiteCheckinDayView{ID: uuid.New(), IndexerID: id, ExpectedAccountID: "18446744073709551615", Profile: ports.NexusPHP1103CheckinProfile, Timezone: "Old/RegistryName", CalendarDay: "2026-10-20", DayStart: start, DayEnd: start.Add(24 * time.Hour), State: "open", ConfigVersion: 1, SourceVersion: 1}
}
func TestCheckinHistoryCursor(t *testing.T) {
	id := uuid.New()
	c := ports.SiteCheckinHistoryCursor{CalendarDay: "2026-10-20", DayID: uuid.New()}
	token := checkinHistoryEncode(id, c)
	got, e := checkinHistoryDecode(id, token)
	if e != nil || *got != c {
		t.Fatal(got, e)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	for _, bad := range []string{string(raw) + " ", strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(raw), `"version":1`, `"version":null`, 1), strings.Replace(string(raw), `"version":1`, `"unknown":1,"version":1`, 1), strings.Replace(string(raw), `"version":1,`, "", 1), strings.Replace(string(raw), "2026-10-20", "2026-02-30", 1), strings.Replace(string(raw), c.DayID.String(), uuid.Nil.String(), 1)} {
		if _, e := checkinHistoryDecode(id, base64.RawURLEncoding.EncodeToString([]byte(bad))); !errors.Is(e, ports.ErrRejected) {
			t.Fatal(bad, e)
		}
	}
	if _, e := checkinHistoryDecode(uuid.New(), token); !errors.Is(e, ports.ErrRejected) {
		t.Fatal(e)
	}
	if _, e := checkinHistoryDecode(id, token+"="); !errors.Is(e, ports.ErrRejected) {
		t.Fatal(e)
	}
}
func TestCheckinHistoryAuthorizationAndLimits(t *testing.T) {
	id := uuid.New()
	calls := 0
	s := NewCheckinHistoryReader(historyRepoFunc(func(_ context.Context, a *ports.Actor, _ uuid.UUID, l int32, _ *ports.SiteCheckinHistoryCursor) (*ports.SiteCheckinHistoryPage, error) {
		calls++
		if l != 20 && l != 100 {
			t.Fatal(l)
		}
		a.Role = "member"
		return &ports.SiteCheckinHistoryPage{}, nil
	}))
	a := historyActor()
	for _, l := range []int32{-1, 101} {
		if _, e := s.Read(t.Context(), a, id, l, ""); !errors.Is(e, ports.ErrRejected) {
			t.Fatal(e)
		}
	}
	for _, bad := range []*ports.Actor{nil, {Type: ports.ActorTypeService, ID: a.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeSystem, ID: a.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: a.ID, Role: ports.RoleMember}, {Type: ports.ActorTypeUser, ID: "urn:uuid:" + a.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: a.ID, Role: ports.RoleAdmin, Scopes: []string{}}} {
		if _, e := s.Read(t.Context(), bad, id, 20, ""); e == nil {
			t.Fatal("authorized invalid actor")
		}
	}
	if calls != 0 {
		t.Fatal(calls)
	}
	for _, l := range []int32{0, 100} {
		if _, e := s.Read(t.Context(), a, id, l, ""); e != nil {
			t.Fatal(e)
		}
	}
	if a.Role != ports.RoleAdmin {
		t.Fatal("mutated caller")
	}
}
func TestCheckinHistoryPageCopyAndPagination(t *testing.T) {
	id := uuid.New()
	row := historyRow(id)
	at := row.DayStart.Add(time.Hour)
	row.State = "attended_observed"
	row.ObservedAt = &at
	page := &ports.SiteCheckinHistoryPage{Rows: []ports.SiteCheckinDayView{row}, Next: &ports.SiteCheckinHistoryCursor{CalendarDay: row.CalendarDay, DayID: row.ID}}
	s := NewCheckinHistoryReader(historyRepoFunc(func(context.Context, *ports.Actor, uuid.UUID, int32, *ports.SiteCheckinHistoryCursor) (*ports.SiteCheckinHistoryPage, error) {
		return page, nil
	}))
	out, e := s.Read(t.Context(), historyActor(), id, 1, "")
	if e != nil || out.NextCursor == "" {
		t.Fatal(out, e)
	}
	out.Rows[0].ExpectedAccountID = "2"
	*out.Rows[0].ObservedAt = time.Time{}
	if page.Rows[0].ExpectedAccountID != row.ExpectedAccountID || at.IsZero() {
		t.Fatal("aliased output")
	}
	if _, e = s.Read(t.Context(), historyActor(), id, 1, out.NextCursor); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("cursor boundary not checked", e)
	}
}
func TestCheckinHistoryContextAndRedaction(t *testing.T) {
	id := uuid.New()
	ctx, cancel := context.WithCancel(t.Context())
	s := NewCheckinHistoryReader(historyRepoFunc(func(context.Context, *ports.Actor, uuid.UUID, int32, *ports.SiteCheckinHistoryCursor) (*ports.SiteCheckinHistoryPage, error) {
		return nil, fmt.Errorf("private cookie URL")
	}))
	if _, e := s.Read(ctx, historyActor(), id, 1, ""); e != ports.ErrUnavailable {
		t.Fatal(e)
	}
	cancel()
	if _, e := s.Read(ctx, historyActor(), id, 1, ""); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	s = NewCheckinHistoryReader(historyRepoFunc(func(context.Context, *ports.Actor, uuid.UUID, int32, *ports.SiteCheckinHistoryCursor) (*ports.SiteCheckinHistoryPage, error) {
		return nil, ports.ErrForbidden
	}))
	if _, e := s.Read(t.Context(), historyActor(), id, 1, ""); e != ports.ErrForbidden {
		t.Fatal(e)
	}
}
func TestCheckinHistoryConcurrentReads(t *testing.T) {
	id := uuid.New()
	a := historyActor()
	s := NewCheckinHistoryReader(historyRepoFunc(func(_ context.Context, a *ports.Actor, _ uuid.UUID, _ int32, _ *ports.SiteCheckinHistoryCursor) (*ports.SiteCheckinHistoryPage, error) {
		a.ID = "local"
		return &ports.SiteCheckinHistoryPage{}, nil
	}))
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, e := s.Read(t.Context(), a, id, 0, ""); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if a.ID == "local" {
		t.Fatal("caller mutated")
	}
}
