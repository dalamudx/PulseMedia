package watchlist_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/watchlist"
	"github.com/google/uuid"
)

type configs struct {
	current                  *ports.DoubanSyncConfig
	receipt                  *ports.DoubanSyncConfigReceipt
	reads, saves, recoveries int
	err                      error
	change                   func(*ports.Actor, ports.DoubanSyncInput)
	cancel                   context.CancelFunc
}

func (c *configs) ReadDoubanSyncConfig(_ context.Context, a *ports.Actor) (*ports.DoubanSyncConfig, error) {
	c.reads++
	if c.change != nil {
		c.change(a, ports.DoubanSyncInput{})
	}
	if c.cancel != nil {
		c.cancel()
	}
	return c.current, c.err
}
func (c *configs) SaveDoubanSyncConfig(_ context.Context, a *ports.Actor, in ports.DoubanSyncInput, _ string) (*ports.DoubanSyncConfigReceipt, error) {
	c.saves++
	if c.change != nil {
		c.change(a, in)
	}
	if c.cancel != nil {
		c.cancel()
	}
	return c.receipt, c.err
}
func (c *configs) ReadDoubanSyncConfigReceipt(_ context.Context, a *ports.Actor, _ string) (*ports.DoubanSyncConfigReceipt, error) {
	c.recoveries++
	if c.change != nil {
		c.change(a, ports.DoubanSyncInput{})
	}
	if c.cancel != nil {
		c.cancel()
	}
	return c.receipt, c.err
}
func configFixture() (*ports.Actor, ports.DoubanSyncInput, *configs) {
	a := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	root := uuid.New()
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	in := ports.DoubanSyncInput{DoubanUID: "exherb", Enabled: true, MovieRootID: &root}
	cfg := ports.DoubanSyncConfig{ID: uuid.New(), OwnerID: uuid.MustParse(a.ID), DoubanUID: in.DoubanUID, Enabled: true, MovieRootID: &root, Version: 1, CreatedAt: now, UpdatedAt: now}
	return a, in, &configs{current: &cfg, receipt: &ports.DoubanSyncConfigReceipt{CommandKey: "original-key", Config: cfg}}
}
func TestConfigCommandsAndReadOnlyRecovery(t *testing.T) {
	a, in, r := configFixture()
	s := watchlist.NewConfigService(r)
	out, err := s.Save(t.Context(), a, in, "original-key")
	if err != nil || out.Config.Version != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	root := *r.receipt.Config.MovieRootID
	*out.Config.MovieRootID = uuid.New()
	if *r.receipt.Config.MovieRootID != root {
		t.Fatal("save aliases repository snapshot")
	}
	r.current.Enabled = false
	r.current.Version = 2
	out, err = s.Receipt(t.Context(), a, "original-key")
	if err != nil || out.Config.Version != 1 || !out.Config.Enabled || r.saves != 1 || r.recoveries != 1 || r.reads != 0 {
		t.Fatalf("%+v %v", out, err)
	}
	*out.Config.MovieRootID = uuid.New()
	if *r.receipt.Config.MovieRootID != root {
		t.Fatal("receipt aliases repository")
	}
	current, err := s.Read(t.Context(), a)
	if err != nil || current == nil || current.Enabled || current.Version != 2 {
		t.Fatalf("%+v %v", current, err)
	}
	*current.MovieRootID = uuid.New()
	if *r.current.MovieRootID != root {
		t.Fatal("read aliases repository")
	}
	r.current = nil
	current, err = s.Read(t.Context(), a)
	if current != nil || err != nil {
		t.Fatal("absent config differs")
	}
}
func TestConfigRejectsBeforeRepository(t *testing.T) {
	cases := []struct {
		name string
		edit func(*ports.Actor, *ports.DoubanSyncInput)
		key  string
		want error
	}{
		{"system", func(a *ports.Actor, _ *ports.DoubanSyncInput) { a.Type = ports.ActorTypeSystem }, "original-key", ports.ErrForbidden},
		{"token", func(a *ports.Actor, _ *ports.DoubanSyncInput) { a.Scopes = []string{} }, "original-key", ports.ErrForbidden},
		{"role", func(a *ports.Actor, _ *ports.DoubanSyncInput) { a.Role = "superadmin" }, "original-key", ports.ErrForbidden},
		{"actor_uuid", func(a *ports.Actor, _ *ports.DoubanSyncInput) { a.ID = "invalid" }, "original-key", ports.ErrForbidden},
		{"url_uid", func(_ *ports.Actor, i *ports.DoubanSyncInput) { i.DoubanUID = "https://douban.com/people/x" }, "original-key", ports.ErrRejected},
		{"traversal", func(_ *ports.Actor, i *ports.DoubanSyncInput) { i.DoubanUID = "../other" }, "original-key", ports.ErrRejected},
		{"uid_blank", func(_ *ports.Actor, i *ports.DoubanSyncInput) { i.DoubanUID = "" }, "original-key", ports.ErrRejected},
		{"uid_long", func(_ *ports.Actor, i *ports.DoubanSyncInput) { i.DoubanUID = strings.Repeat("a", 65) }, "original-key", ports.ErrRejected},
		{"negative_version", func(_ *ports.Actor, i *ports.DoubanSyncInput) { i.ExpectedVersion = -1 }, "original-key", ports.ErrRejected},
		{"version_overflow", func(_ *ports.Actor, i *ports.DoubanSyncInput) { i.ExpectedVersion = math.MaxInt64 }, "original-key", ports.ErrRejected},
		{"nil_uuid", func(_ *ports.Actor, i *ports.DoubanSyncInput) { x := uuid.Nil; i.MovieRootID = &x }, "original-key", ports.ErrRejected},
		{"no_root", func(_ *ports.Actor, i *ports.DoubanSyncInput) { i.MovieRootID = nil }, "original-key", ports.ErrRejected},
		{"same_roots", func(_ *ports.Actor, i *ports.DoubanSyncInput) { i.TVRootID = i.MovieRootID }, "original-key", ports.ErrRejected},
		{"blank_key", func(*ports.Actor, *ports.DoubanSyncInput) {}, "", ports.ErrRejected},
		{"bad_key", func(*ports.Actor, *ports.DoubanSyncInput) {}, "key\n", ports.ErrRejected},
		{"long_key", func(*ports.Actor, *ports.DoubanSyncInput) {}, strings.Repeat("k", 161), ports.ErrRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, in, r := configFixture()
			c.edit(a, &in)
			out, err := watchlist.NewConfigService(r).Save(t.Context(), a, in, c.key)
			if !errors.Is(err, c.want) || out != nil || r.saves != 0 || r.reads != 0 || r.recoveries != 0 {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
	t.Run("anonymous", func(t *testing.T) {
		_, in, r := configFixture()
		_, err := watchlist.NewConfigService(r).Save(t.Context(), nil, in, "original-key")
		if !errors.Is(err, ports.ErrUnauthorized) || r.saves != 0 {
			t.Fatal(err)
		}
	})
	t.Run("disabled_without_roots", func(t *testing.T) {
		a, in, r := configFixture()
		in.Enabled = false
		in.MovieRootID = nil
		r.receipt.Config.Enabled = false
		r.receipt.Config.MovieRootID = nil
		out, err := watchlist.NewConfigService(r).Save(t.Context(), a, in, "original-key")
		if err != nil || out == nil || r.saves != 1 {
			t.Fatalf("%+v %v", out, err)
		}
	})
}
func TestConfigReceiptAndInputFences(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*configs)
	}{
		{"nil", func(r *configs) { r.receipt = nil }},
		{"owner", func(r *configs) { r.receipt.Config.OwnerID = uuid.New() }},
		{"key", func(r *configs) { r.receipt.CommandKey = "different" }},
		{"version", func(r *configs) { r.receipt.Config.Version = 2 }},
		{"uid", func(r *configs) { r.receipt.Config.DoubanUID = "different" }},
		{"enabled", func(r *configs) { r.receipt.Config.Enabled = false }},
		{"root", func(r *configs) { x := uuid.New(); r.receipt.Config.MovieRootID = &x }},
		{"time", func(r *configs) { r.receipt.Config.UpdatedAt = r.receipt.Config.CreatedAt.Add(-time.Second) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, in, r := configFixture()
			c.edit(r)
			out, err := watchlist.NewConfigService(r).Save(t.Context(), a, in, "original-key")
			if !errors.Is(err, ports.ErrUnavailable) || out != nil {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
	t.Run("collaborator_mutation", func(t *testing.T) {
		a, in, r := configFixture()
		owner := a.ID
		root := *in.MovieRootID
		r.change = func(actor *ports.Actor, input ports.DoubanSyncInput) {
			actor.ID = uuid.NewString()
			*input.MovieRootID = uuid.New()
		}
		out, err := watchlist.NewConfigService(r).Save(t.Context(), a, in, "original-key")
		if err != nil || out.Config.OwnerID.String() != owner || a.ID != owner || *in.MovieRootID != root {
			t.Fatalf("%+v %v", out, err)
		}
	})
}
func TestConfigErrorsAndLateCancellation(t *testing.T) {
	for _, method := range []string{"read", "save", "receipt"} {
		t.Run(method, func(t *testing.T) {
			call := func(ctx context.Context, s *watchlist.ConfigService, a *ports.Actor, in ports.DoubanSyncInput) error {
				switch method {
				case "read":
					_, e := s.Read(ctx, a)
					return e
				case "save":
					_, e := s.Save(ctx, a, in, "original-key")
					return e
				default:
					_, e := s.Receipt(ctx, a, "original-key")
					return e
				}
			}
			for _, e := range []error{errors.New("database secret detail"), ports.ErrForbidden, ports.ErrSubscriptionBindingsConflict, ports.ErrNotFound} {
				a, in, r := configFixture()
				r.err = e
				err := call(t.Context(), watchlist.NewConfigService(r), a, in)
				want := e
				if e.Error() == "database secret detail" {
					want = ports.ErrUnavailable
				}
				if !errors.Is(err, want) || strings.Contains(err.Error(), "secret") {
					t.Fatal(err)
				}
			}
			a, in, r := configFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r.cancel = cancel
			err := call(ctx, watchlist.NewConfigService(r), a, in)
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
