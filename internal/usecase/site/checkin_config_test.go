package site_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/google/uuid"
)

type checkinConfigRepo struct {
	calls   int
	config  *ports.SiteCheckinConfig
	receipt *ports.SiteCheckinConfigReceipt
	err     error
	before  func(*ports.Actor)
}

func (r *checkinConfigRepo) call(a *ports.Actor) {
	r.calls++
	if r.before != nil {
		r.before(a)
	}
}
func (r *checkinConfigRepo) ReadSiteCheckinConfig(_ context.Context, a *ports.Actor, _ uuid.UUID) (*ports.SiteCheckinConfig, error) {
	r.call(a)
	return r.config, r.err
}
func (r *checkinConfigRepo) SaveSiteCheckinConfig(_ context.Context, a *ports.Actor, _ uuid.UUID, _ ports.SiteCheckinConfigInput, _ string) (*ports.SiteCheckinConfigReceipt, error) {
	r.call(a)
	return r.receipt, r.err
}
func (r *checkinConfigRepo) ReadSiteCheckinConfigReceipt(_ context.Context, a *ports.Actor, _ uuid.UUID, _ string) (*ports.SiteCheckinConfigReceipt, error) {
	r.call(a)
	return r.receipt, r.err
}
func checkinConfigFixture() (ports.Actor, uuid.UUID, ports.SiteCheckinConfigInput, *checkinConfigRepo) {
	a, id, _, old := accountStatsConfigFixture()
	in := ports.SiteCheckinConfigInput{ExpectedAccountID: "42", Profile: ports.NexusPHP1103CheckinProfile, Timezone: "Asia/Shanghai", DailyAt: "09:00", Enabled: true, ExpectedSourceVersion: 5}
	c := ports.SiteCheckinConfig{IndexerID: id, ExpectedAccountID: in.ExpectedAccountID, Profile: in.Profile, Timezone: in.Timezone, DailyAt: in.DailyAt, Enabled: in.Enabled, Version: 1, SourceConfigVersion: in.ExpectedSourceVersion, CreatedAt: old.config.CreatedAt, UpdatedAt: old.config.UpdatedAt}
	return a, id, in, &checkinConfigRepo{config: &c, receipt: &ports.SiteCheckinConfigReceipt{CommandKey: "original-key", Config: c}}
}
func TestCheckinConfigAdmission(t *testing.T) {
	a, id, in, _ := checkinConfigFixture()
	member, token, system := a, a, a
	member.Role = ports.RoleMember
	token.Scopes = []string{"admin"}
	system.Type = ports.ActorTypeSystem
	for _, tc := range []struct {
		name   string
		actor  *ports.Actor
		source uuid.UUID
		want   error
	}{
		{"anonymous", nil, id, ports.ErrUnauthorized},
		{"member", &member, id, ports.ErrForbidden},
		{"token", &token, id, ports.ErrForbidden},
		{"system", &system, id, ports.ErrForbidden},
		{"missing source", &a, uuid.Nil, ports.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &checkinConfigRepo{}
			s := site.NewCheckinConfigService(r)
			_, e := s.Read(t.Context(), tc.actor, tc.source)
			if !errors.Is(e, tc.want) {
				t.Fatal(e)
			}
			_, e = s.Save(t.Context(), tc.actor, tc.source, in, "key")
			if !errors.Is(e, tc.want) {
				t.Fatal(e)
			}
			_, e = s.Receipt(t.Context(), tc.actor, tc.source, "key")
			if !errors.Is(e, tc.want) || r.calls != 0 {
				t.Fatal(e, r.calls)
			}
		})
	}
	if _, e := site.NewCheckinConfigService(nil).Read(t.Context(), &a, id); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
}
func TestCheckinConfigInvalidBeforeRepository(t *testing.T) {
	a, id, in, r := checkinConfigFixture()
	s := site.NewCheckinConfigService(r)
	for name, change := range map[string]func(*ports.SiteCheckinConfigInput){
		"UID":            func(v *ports.SiteCheckinConfigInput) { v.ExpectedAccountID = "042" },
		"profile":        func(v *ports.SiteCheckinConfigInput) { v.Profile = ports.NexusPHPClassicAccountStatsProfile },
		"host timezone":  func(v *ports.SiteCheckinConfigInput) { v.Timezone = "Local" },
		"clock":          func(v *ports.SiteCheckinConfigInput) { v.DailyAt = "24:00" },
		"version":        func(v *ports.SiteCheckinConfigInput) { v.ExpectedVersion = -1 },
		"overflow":       func(v *ports.SiteCheckinConfigInput) { v.ExpectedVersion = math.MaxInt64 },
		"source version": func(v *ports.SiteCheckinConfigInput) { v.ExpectedSourceVersion = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			v := in
			change(&v)
			if _, e := s.Save(t.Context(), &a, id, v, "key"); !errors.Is(e, ports.ErrRejected) {
				t.Fatal(e)
			}
		})
	}
	if _, e := s.Save(t.Context(), &a, id, in, "bad/key"); !errors.Is(e, ports.ErrRejected) || r.calls != 0 {
		t.Fatal(e, r.calls)
	}
}
func TestCheckinConfigCopiesAndHistoricalReceipt(t *testing.T) {
	a, id, in, r := checkinConfigFixture()
	s := site.NewCheckinConfigService(r)
	oldID := a.ID
	r.before = func(actor *ports.Actor) { actor.ID = uuid.NewString() }
	c, e := s.Read(t.Context(), &a, id)
	if e != nil || a.ID != oldID {
		t.Fatal(e, a.ID)
	}
	c.Timezone = "UTC"
	if r.config.Timezone != "Asia/Shanghai" {
		t.Fatal("read aliases repository")
	}
	saved, e := s.Save(t.Context(), &a, id, in, "original-key")
	if e != nil {
		t.Fatal(e)
	}
	saved.Config.DailyAt = "18:00"
	if r.receipt.Config.DailyAt != "09:00" {
		t.Fatal("receipt aliases repository")
	}
	r.config.Version = 2
	r.config.Enabled = false
	history, e := s.Receipt(t.Context(), &a, id, "original-key")
	if e != nil || history.Config.Version != 1 || !history.Config.Enabled {
		t.Fatal(history, e)
	}
	r.config = nil
	if c, e = s.Read(t.Context(), &a, id); e != nil || c != nil {
		t.Fatal(c, e)
	}
}
func TestCheckinConfigResultFences(t *testing.T) {
	for name, change := range map[string]func(*ports.SiteCheckinConfigReceipt){
		"key":            func(v *ports.SiteCheckinConfigReceipt) { v.CommandKey = "other" },
		"source":         func(v *ports.SiteCheckinConfigReceipt) { v.Config.IndexerID = uuid.New() },
		"source version": func(v *ports.SiteCheckinConfigReceipt) { v.Config.SourceConfigVersion++ },
		"version":        func(v *ports.SiteCheckinConfigReceipt) { v.Config.Version++ },
		"timezone":       func(v *ports.SiteCheckinConfigReceipt) { v.Config.Timezone = "UTC" },
		"clock":          func(v *ports.SiteCheckinConfigReceipt) { v.Config.DailyAt = "09:01" },
		"account":        func(v *ports.SiteCheckinConfigReceipt) { v.Config.ExpectedAccountID = "43" },
	} {
		t.Run(name, func(t *testing.T) {
			a, id, in, r := checkinConfigFixture()
			change(r.receipt)
			if _, e := site.NewCheckinConfigService(r).Save(t.Context(), &a, id, in, "original-key"); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal(e)
			}
		})
	}
	a, id, in, r := checkinConfigFixture()
	r.err = errors.New("database private credential detail")
	if _, e := site.NewCheckinConfigService(r).Save(t.Context(), &a, id, in, "original-key"); e != ports.ErrUnavailable {
		t.Fatal(e)
	}
	r.err = ports.ErrEndpointVersionConflict
	if _, e := site.NewCheckinConfigService(r).Save(t.Context(), &a, id, in, "original-key"); e != ports.ErrEndpointVersionConflict {
		t.Fatal(e)
	}
}
func TestCheckinConfigLateCancellation(t *testing.T) {
	for _, action := range []string{"read", "save", "receipt"} {
		t.Run(action, func(t *testing.T) {
			a, id, in, r := checkinConfigFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r.before = func(*ports.Actor) { cancel() }
			s := site.NewCheckinConfigService(r)
			var e error
			switch action {
			case "read":
				_, e = s.Read(ctx, &a, id)
			case "save":
				_, e = s.Save(ctx, &a, id, in, "original-key")
			case "receipt":
				_, e = s.Receipt(ctx, &a, id, "original-key")
			}
			if !errors.Is(e, context.Canceled) || r.calls != 1 {
				t.Fatal(e, r.calls)
			}
		})
	}
}
