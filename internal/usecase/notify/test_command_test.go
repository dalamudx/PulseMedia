package notify_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"testing"
	"time"
)

type notificationTestRepo struct {
	calls    int
	hash     string
	expected time.Time
}

func (r *notificationTestRepo) QueueNotificationTest(_ context.Context, _ *ports.Actor, id uuid.UUID, expected time.Time, key, hash string) (*ports.NotificationTestReceipt, error) {
	r.calls++
	r.hash = hash
	r.expected = expected
	return &ports.NotificationTestReceipt{ChannelID: id, EventID: uuid.New(), Status: "queued"}, nil
}
func TestNotificationTestAuthority(t *testing.T) {
	repo := &notificationTestRepo{}
	service := notify.NewTestService(repo)
	id := uuid.New()
	now := time.Now()
	for _, a := range []*ports.Actor{nil, {Type: ports.ActorTypeUser, Role: ports.RoleMember}, {Type: ports.ActorTypeUser, Role: ports.RoleAdmin, Scopes: []string{}}, {Type: ports.ActorTypeSystem, Role: ports.RoleAdmin}} {
		_, e := service.Queue(t.Context(), a, id, now, "key")
		if !errors.Is(e, ports.ErrUnauthorized) && !errors.Is(e, ports.ErrForbidden) {
			t.Fatal(e)
		}
	}
	if repo.calls != 0 {
		t.Fatal("invalid authority entered repository")
	}
}
func TestNotificationTestCanonicalInput(t *testing.T) {
	repo := &notificationTestRepo{}
	service := notify.NewTestService(repo)
	actor := &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	id := uuid.New()
	now := time.Date(2026, 10, 22, 12, 0, 0, 123000, time.UTC)
	if _, e := service.Queue(t.Context(), actor, id, now, "key"); e != nil {
		t.Fatal(e)
	}
	hash := repo.hash
	if _, e := service.Queue(t.Context(), actor, id, now.In(time.FixedZone("east", 8*3600)), "key"); e != nil || repo.hash != hash || repo.expected.Location() != time.UTC {
		t.Fatal("equivalent timestamps changed original key")
	}
	if _, e := service.Queue(t.Context(), actor, id, now.Add(time.Second), "key"); e != nil || repo.hash == hash {
		t.Fatal("changed version has same fingerprint")
	}
}
func TestNotificationTestInvalidInput(t *testing.T) {
	repo := &notificationTestRepo{}
	service := notify.NewTestService(repo)
	actor := &ports.Actor{Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	for _, c := range []struct {
		id   uuid.UUID
		when time.Time
		key  string
	}{{uuid.Nil, time.Now(), "key"}, {uuid.New(), time.Time{}, "key"}, {uuid.New(), time.Now(), ""}} {
		if _, e := service.Queue(t.Context(), actor, c.id, c.when, c.key); !errors.Is(e, notify.ErrInvalidConfig) {
			t.Fatal(e)
		}
	}
	if repo.calls != 0 {
		t.Fatal("invalid command wrote")
	}
	if _, e := notify.NewTestService(nil).Queue(t.Context(), actor, uuid.New(), time.Now(), "key"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
}
