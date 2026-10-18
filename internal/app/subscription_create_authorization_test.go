package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestSubscriptionCreateCurrentUserAfterLock(t *testing.T) {
	f := organizationSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id := uuid.New()
	if _, e := f.q.CreateUser(ctx, db.CreateUserParams{ID: id, Username: "create-current-user", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	svcStore := postgres.NewSubscriptionIntentStore(f.pool, f.gen)
	svc := subscription.NewService(svcStore, svcStore, postgres.NewSubscriptionControlStore(f.pool, f.gen), svcStore)
	cmd := subscription.CreateSubscriptionCommand{MediaID: f.media, RootID: f.root, ScopeType: "all"}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleMember}
	for _, a := range []*ports.Actor{nil, {Type: ports.ActorTypeService, ID: id.String(), Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleMember, Scopes: []string{"read"}}, {Type: ports.ActorTypeUser, ID: id.String(), Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: uuid.New().String(), Role: ports.RoleMember}} {
		if _, _, e := svc.CreateSubscription(ctx, a, cmd); !errors.Is(e, ports.ErrForbidden) && !errors.Is(e, ports.ErrUnauthorized) {
			t.Fatal("invalid current actor accepted", a, e)
		}
	}
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, _, e := svc.CreateSubscription(ctx, actor, cmd); done <- e }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if e = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%' AND pid<>pg_backend_pid())`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("user row lock not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("post-lock deactivation accepted", e)
	}
	var count int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM subscription WHERE user_id=$1", id).Scan(&count); e != nil || count != 0 {
		t.Fatal("denied create wrote business", count, e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE app_user SET is_active=true WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	if _, _, e = svc.CreateSubscription(ctx, actor, cmd); e != nil {
		t.Fatal("restored member denied", e)
	}
}
