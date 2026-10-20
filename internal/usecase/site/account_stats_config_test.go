package site_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/site"
	"github.com/google/uuid"
)

type accountStatsConfigRepo struct {
	calls   int
	config  *ports.SiteAccountStatsConfig
	receipt *ports.SiteAccountStatsConfigReceipt
	err     error
	before  func(*ports.Actor)
}

func (r *accountStatsConfigRepo) call(a *ports.Actor) {
	r.calls++
	if r.before != nil {
		r.before(a)
	}
}
func (r *accountStatsConfigRepo) ReadSiteAccountStatsConfig(_ context.Context, a *ports.Actor, _ uuid.UUID) (*ports.SiteAccountStatsConfig, error) {
	r.call(a)
	return r.config, r.err
}
func (r *accountStatsConfigRepo) SaveSiteAccountStatsConfig(_ context.Context, a *ports.Actor, _ uuid.UUID, _ ports.SiteAccountStatsConfigInput, _ string) (*ports.SiteAccountStatsConfigReceipt, error) {
	r.call(a)
	return r.receipt, r.err
}
func (r *accountStatsConfigRepo) ReadSiteAccountStatsConfigReceipt(_ context.Context, a *ports.Actor, _ uuid.UUID, _ string) (*ports.SiteAccountStatsConfigReceipt, error) {
	r.call(a)
	return r.receipt, r.err
}

func accountStatsConfigFixture() (ports.Actor, uuid.UUID, ports.SiteAccountStatsConfigInput, *accountStatsConfigRepo) {
	a := ports.Actor{ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	id := uuid.New()
	in := ports.SiteAccountStatsConfigInput{ExpectedAccountID: "42", Profile: ports.NexusPHPClassicAccountStatsProfile, Enabled: true, IntervalSeconds: 900, ExpectedSourceVersion: 5}
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	c := ports.SiteAccountStatsConfig{IndexerID: id, ExpectedAccountID: in.ExpectedAccountID, Profile: in.Profile, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, SourceConfigVersion: in.ExpectedSourceVersion, Version: 1, CreatedAt: now, UpdatedAt: now}
	return a, id, in, &accountStatsConfigRepo{config: &c, receipt: &ports.SiteAccountStatsConfigReceipt{CommandKey: "original-key", Config: c}}
}

func TestAccountStatsConfigAuthorityBeforeRepository(t *testing.T) {
	a, id, in, _ := accountStatsConfigFixture()
	actors := []*ports.Actor{nil, {Type: ports.ActorTypeUser, ID: a.ID, Role: ports.RoleMember}, {Type: ports.ActorTypeSystem, ID: a.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: a.ID, Role: ports.RoleAdmin, Scopes: []string{}}, {Type: ports.ActorTypeUser, ID: "not-uuid", Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: uuid.Nil.String(), Role: ports.RoleAdmin}}
	for i, actor := range actors {
		for _, method := range []string{"read", "save", "receipt"} {
			t.Run(method+string(rune('A'+i)), func(t *testing.T) {
				r := &accountStatsConfigRepo{}
				s := site.NewAccountStatsConfigService(r)
				var e error
				switch method {
				case "read":
					_, e = s.Read(context.Background(), actor, id)
				case "save":
					_, e = s.Save(context.Background(), actor, id, in, "original-key")
				case "receipt":
					_, e = s.Receipt(context.Background(), actor, id, "original-key")
				}
				want := ports.ErrForbidden
				if actor == nil {
					want = ports.ErrUnauthorized
				}
				if !errors.Is(e, want) || r.calls != 0 {
					t.Fatalf("%v calls=%d", e, r.calls)
				}
			})
		}
	}
	s := site.NewAccountStatsConfigService(nil)
	if _, e := s.Read(context.Background(), &a, uuid.Nil); !errors.Is(e, ports.ErrRejected) {
		t.Fatal(e)
	}
	if _, e := s.Read(context.Background(), &a, id); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
}

func TestAccountStatsConfigInputRejectedBeforeRepository(t *testing.T) {
	a, id, in, r := accountStatsConfigFixture()
	s := site.NewAccountStatsConfigService(r)
	for name, change := range map[string]func(*ports.SiteAccountStatsConfigInput){
		"empty_uid": func(x *ports.SiteAccountStatsConfigInput) { x.ExpectedAccountID = "" }, "leading_zero": func(x *ports.SiteAccountStatsConfigInput) { x.ExpectedAccountID = "042" }, "overflow_uid": func(x *ports.SiteAccountStatsConfigInput) { x.ExpectedAccountID = "18446744073709551616" },
		"unknown_profile": func(x *ports.SiteAccountStatsConfigInput) { x.Profile = "torznab" }, "fast": func(x *ports.SiteAccountStatsConfigInput) { x.IntervalSeconds = 899 }, "slow": func(x *ports.SiteAccountStatsConfigInput) { x.IntervalSeconds = 86401 }, "negative_version": func(x *ports.SiteAccountStatsConfigInput) { x.ExpectedVersion = -1 }, "overflow_version": func(x *ports.SiteAccountStatsConfigInput) { x.ExpectedVersion = math.MaxInt64 }, "missing_source_version": func(x *ports.SiteAccountStatsConfigInput) { x.ExpectedSourceVersion = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			v := in
			change(&v)
			if _, e := s.Save(context.Background(), &a, id, v, "original-key"); !errors.Is(e, ports.ErrRejected) {
				t.Fatal(e)
			}
		})
	}
	for _, key := range []string{"", "bad key", "bad/key", "bad?key", "密钥", "line\nkey", strings.Repeat("k", 161)} {
		t.Run(key, func(t *testing.T) {
			if _, e := s.Save(context.Background(), &a, id, in, key); !errors.Is(e, ports.ErrRejected) {
				t.Fatal(e)
			}
			if _, e := s.Receipt(context.Background(), &a, id, key); !errors.Is(e, ports.ErrRejected) {
				t.Fatal(e)
			}
		})
	}
	if r.calls != 0 {
		t.Fatal(r.calls)
	}
}

func TestAccountStatsConfigCopiesAndOriginalReceipt(t *testing.T) {
	a, id, in, r := accountStatsConfigFixture()
	s := site.NewAccountStatsConfigService(r)
	r.before = func(copy *ports.Actor) { copy.ID = uuid.NewString(); copy.Role = ports.RoleMember }
	got, e := s.Read(context.Background(), &a, id)
	if e != nil {
		t.Fatal(e)
	}
	got.ExpectedAccountID = "99"
	if r.config.ExpectedAccountID != "42" || a.Role != ports.RoleAdmin {
		t.Fatal("shared config or actor")
	}
	saved, e := s.Save(context.Background(), &a, id, in, "original-key")
	if e != nil {
		t.Fatal(e)
	}
	saved.Config.Version = 100
	if r.receipt.Config.Version != 1 {
		t.Fatal("shared receipt")
	}
	r.config.Version = 2
	r.config.Enabled = false
	recovered, e := s.Receipt(context.Background(), &a, id, "original-key")
	if e != nil || recovered.Config.Version != 1 || !recovered.Config.Enabled {
		t.Fatalf("%+v %v", recovered, e)
	}
	r.config = nil
	if c, e := s.Read(context.Background(), &a, id); c != nil || e != nil {
		t.Fatalf("%+v %v", c, e)
	}
}

func TestAccountStatsConfigResultFenceAndSafeErrors(t *testing.T) {
	for name, change := range map[string]func(*ports.SiteAccountStatsConfigReceipt){
		"wrong_source": func(x *ports.SiteAccountStatsConfigReceipt) { x.Config.IndexerID = uuid.New() }, "wrong_key": func(x *ports.SiteAccountStatsConfigReceipt) { x.CommandKey = "different-key" }, "wrong_version": func(x *ports.SiteAccountStatsConfigReceipt) { x.Config.Version = 2 }, "source_version": func(x *ports.SiteAccountStatsConfigReceipt) { x.Config.SourceConfigVersion = 6 }, "wrong_account": func(x *ports.SiteAccountStatsConfigReceipt) { x.Config.ExpectedAccountID = "99" }, "wrong_enabled": func(x *ports.SiteAccountStatsConfigReceipt) { x.Config.Enabled = false }, "wrong_interval": func(x *ports.SiteAccountStatsConfigReceipt) { x.Config.IntervalSeconds = 1800 }, "bad_profile": func(x *ports.SiteAccountStatsConfigReceipt) { x.Config.Profile = "guessed" }, "zero_timestamp": func(x *ports.SiteAccountStatsConfigReceipt) { x.Config.CreatedAt = time.Time{} }, "backwards_time": func(x *ports.SiteAccountStatsConfigReceipt) {
			x.Config.UpdatedAt = x.Config.CreatedAt.Add(-time.Second)
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, id, in, r := accountStatsConfigFixture()
			change(r.receipt)
			if _, e := site.NewAccountStatsConfigService(r).Save(context.Background(), &a, id, in, "original-key"); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal(e)
			}
		})
	}
	a, id, in, r := accountStatsConfigFixture()
	s := site.NewAccountStatsConfigService(r)
	for _, e := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrRejected, ports.ErrNotFound, ports.ErrEndpointVersionConflict, errors.New("postgres://credential@private-source.invalid")} {
		r.err = e
		want := e
		if strings.Contains(e.Error(), "private-source") {
			want = ports.ErrUnavailable
		}
		if _, got := s.Save(context.Background(), &a, id, in, "original-key"); got != want {
			t.Fatal(got)
		}
		if _, got := s.Read(context.Background(), &a, id); got != want {
			t.Fatal(got)
		}
		if _, got := s.Receipt(context.Background(), &a, id, "original-key"); got != want {
			t.Fatal(got)
		}
	}
	r.err = nil
	r.receipt = nil
	if _, e := s.Receipt(context.Background(), &a, id, "original-key"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
}

func TestAccountStatsConfigCancellationRejectsLateSuccess(t *testing.T) {
	for _, method := range []string{"read", "save", "receipt"} {
		for _, late := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "_before", true: "_after"}[late], func(t *testing.T) {
				a, id, in, r := accountStatsConfigFixture()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if late {
					r.before = func(*ports.Actor) { cancel() }
				} else {
					cancel()
				}
				s := site.NewAccountStatsConfigService(r)
				var e error
				switch method {
				case "read":
					_, e = s.Read(ctx, &a, id)
				case "save":
					_, e = s.Save(ctx, &a, id, in, "original-key")
				case "receipt":
					_, e = s.Receipt(ctx, &a, id, "original-key")
				}
				if !errors.Is(e, context.Canceled) || (!late && r.calls != 0) || (late && r.calls != 1) {
					t.Fatalf("%v calls=%d", e, r.calls)
				}
			})
		}
	}
}
