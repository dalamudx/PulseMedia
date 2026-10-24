package seeding_test

import (
	"context"
	"errors"
	rule "github.com/dalamudx/PulseMedia/internal/domain/seeding"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/seeding"
	"github.com/google/uuid"
	"math"
	"strings"
	"testing"
	"time"
)

type configRepo struct {
	view                   *ports.SeedingConfigView
	original               *ports.SeedingConfigReceipt
	err, saveErr           error
	reads, lookups, writes int
	input                  ports.SeedingConfigInput
	hash                   string
	hook                   func()
}

func (r *configRepo) ReadSeedingConfig(context.Context, *ports.Actor, uuid.UUID) (*ports.SeedingConfigView, error) {
	r.reads++
	if r.hook != nil {
		r.hook()
	}
	return r.view, r.err
}
func (r *configRepo) ReadSeedingConfigReceipt(_ context.Context, _ *ports.Actor, _ uuid.UUID, _ string, hash *string) (*ports.SeedingConfigReceipt, error) {
	r.lookups++
	if r.hook != nil {
		r.hook()
	}
	if hash != nil && r.hash != "" && r.hash != *hash {
		return nil, ports.ErrSeedingConfigConflict
	}
	return r.original, r.err
}
func (r *configRepo) SaveSeedingConfig(_ context.Context, a *ports.Actor, id uuid.UUID, in ports.SeedingConfigInput, key, hash string) (*ports.SeedingConfigReceipt, error) {
	r.writes++
	r.input = in
	if r.saveErr != nil {
		return nil, r.saveErr
	}
	r.hash = hash
	r.view = &ports.SeedingConfigView{ClientID: id, OwnerID: uuid.MustParse(a.ID), Version: in.ExpectedVersion + 1, ClientUpdatedAt: in.ExpectedClientUpdatedAt, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, Rule: in.Rule, UpdatedAt: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)}
	r.original = &ports.SeedingConfigReceipt{CommandKey: key, Config: *r.view}
	return r.original, nil
}
func setup() (*seeding.ConfigService, *configRepo, *ports.Actor, uuid.UUID, ports.SeedingConfigInput) {
	r := &configRepo{}
	a := &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleAdmin, ID: uuid.NewString()}
	ratio := 2.0
	return seeding.NewConfigService(r), r, a, uuid.New(), ports.SeedingConfigInput{ExpectedClientUpdatedAt: time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC), Enabled: true, IntervalSeconds: 60, Rule: rule.Rule{Mode: rule.MatchAny, MinShareRatio: &ratio}}
}
func TestSeedingConfigSaveAndCopyIsolation(t *testing.T) {
	s, r, a, id, in := setup()
	v, err := s.Save(t.Context(), a, id, in, "original")
	if err != nil || v.Config.Version != 1 || v.Config.OwnerID.String() != a.ID || r.writes != 1 || len(r.hash) != 64 {
		t.Fatal(v, err)
	}
	*v.Config.Rule.MinShareRatio = 20
	if *in.Rule.MinShareRatio != 2 || *r.view.Rule.MinShareRatio != 2 {
		t.Fatal("response mutated input or stored snapshot")
	}
	*in.Rule.MinShareRatio = 30
	if *r.view.Rule.MinShareRatio != 2 {
		t.Fatal("request pointer retained")
	}
	current, err := s.Read(t.Context(), a, id)
	if err != nil {
		t.Fatal(err)
	}
	*current.Rule.MinShareRatio = 99
	if *r.view.Rule.MinShareRatio != 2 {
		t.Fatal("read aliases store")
	}
	receipt, err := s.Receipt(t.Context(), a, id, "original")
	if err != nil || *receipt.Config.Rule.MinShareRatio != 2 || r.reads != 1 || r.writes != 1 {
		t.Fatal(receipt, err)
	}
}
func TestSeedingConfigOriginalAndConflicts(t *testing.T) {
	s, r, a, id, in := setup()
	v, err := s.Save(t.Context(), a, id, in, "original")
	if err != nil {
		t.Fatal(err)
	}
	originalVersion := v.Config.Version
	r.view.Version = 8
	again, err := s.Save(t.Context(), a, id, in, "original")
	if err != nil || again.Config.Version != originalVersion || r.writes != 1 || r.reads != 0 {
		t.Fatal("original result rebuilt", again, err)
	}
	in.IntervalSeconds = 120
	if _, err = s.Save(t.Context(), a, id, in, "original"); !errors.Is(err, ports.ErrSeedingConfigConflict) || r.writes != 1 {
		t.Fatal("same key changed input accepted", err)
	}
	r.hash = ""
	in.IntervalSeconds = 0
	if _, err = s.Save(t.Context(), a, id, in, "original"); err != nil || r.writes != 1 {
		t.Fatal("historical lookup ran after new validation", err)
	}
}
func TestSeedingConfigPermissionsAndCancellation(t *testing.T) {
	s, r, a, id, in := setup()
	member := *a
	member.Role = ports.RoleMember
	token := *a
	token.Scopes = []string{"system.admin"}
	scoped := *a
	scoped.Scopes = []string{}
	for _, actor := range []*ports.Actor{nil, &member, &token, &scoped} {
		if _, err := s.Save(t.Context(), actor, id, in, "original"); err == nil {
			t.Fatal("unprivileged save")
		}
		if _, err := s.Read(t.Context(), actor, id); err == nil {
			t.Fatal("unprivileged read")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Save(ctx, a, id, in, "original"); !errors.Is(err, context.Canceled) || r.lookups != 0 || r.writes != 0 {
		t.Fatal("cancel/authority reached store", err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	r.hook = cancel
	if _, err := s.Read(ctx, a, id); !errors.Is(err, context.Canceled) {
		t.Fatal("late read cancellation", err)
	}
}
func TestSeedingConfigValidationMakesNoWrites(t *testing.T) {
	for _, name := range []string{"client", "key", "version", "client-version", "interval", "delete-data", "unknown-mode", "empty-rule", "nan-ratio", "zero-space"} {
		t.Run(name, func(t *testing.T) {
			s, r, a, id, in := setup()
			key := "original"
			switch name {
			case "client":
				id = uuid.Nil
			case "key":
				key = "unsafe\nkey"
			case "version":
				in.ExpectedVersion = math.MaxInt64
			case "client-version":
				in.ExpectedClientUpdatedAt = time.Time{}
			case "interval":
				in.IntervalSeconds = 59
			case "delete-data":
				in.Rule.DeleteFiles = true
			case "unknown-mode":
				in.Rule.Mode = "guess"
			case "empty-rule":
				in.Rule.MinShareRatio = nil
			case "nan-ratio":
				*in.Rule.MinShareRatio = math.NaN()
			case "zero-space":
				n := int64(0)
				in.Rule.LowFreeBytes = &n
			}
			if _, err := s.Save(t.Context(), a, id, in, key); !errors.Is(err, ports.ErrRejected) || r.writes != 0 {
				t.Fatal("invalid input wrote", err)
			}
		})
	}
}
func TestSeedingConfigSafeReadAndStoreErrors(t *testing.T) {
	s, r, a, id, in := setup()
	if v, err := s.Read(t.Context(), a, id); v != nil || err != nil {
		t.Fatal("absent setting failed", err)
	}
	if _, err := s.Receipt(t.Context(), a, id, "missing"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal(err)
	}
	r.err = errors.New("owned-internal-store-detail")
	if _, err := s.Save(t.Context(), a, id, in, "original"); !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "owned-internal") {
		t.Fatal("store detail exposed", err)
	}
	r.err = nil
	r.saveErr = ports.ErrForbidden
	if _, err := s.Save(t.Context(), a, id, in, "original"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("revocation lost", err)
	}
	r.saveErr = nil
	if _, err := s.Save(t.Context(), a, id, in, "original"); err != nil {
		t.Fatal(err)
	}
	r.original.Config.ClientID = uuid.New()
	if _, err := s.Receipt(t.Context(), a, id, "original"); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("foreign receipt accepted", err)
	}
}
