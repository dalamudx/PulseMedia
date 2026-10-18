package httpapi_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
)

func assertRecoveryUserLock(t *testing.T, pool *pgxpool.Pool, actor *ports.Actor, call func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	user := uuid.MustParse(actor.ID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		if committed {
			if _, e := pool.Exec(context.WithoutCancel(ctx), "UPDATE app_user SET is_active=true WHERE id=$1", user); e != nil {
				t.Error(e)
			}
		}
	}()
	if _, err = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", user); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- call(ctx) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case e := <-done:
			t.Fatalf("recovery finished before waiting for current-user update: %v", e)
		case <-ctx.Done():
			t.Fatal("recovery did not reach user lock", ctx.Err())
		case <-ticker.C:
			var waiting bool
			err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid))>0 AND query LIKE '%app_user%' AND pid<>pg_backend_pid())").Scan(&waiting)
			if err != nil {
				t.Fatal(err)
			}
			if !waiting {
				continue
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			committed = true
			select {
			case e := <-done:
				if !errors.Is(e, ports.ErrForbidden) {
					t.Fatalf("disabled user recovered preview or receipt: %v", e)
				}
			case <-ctx.Done():
				t.Fatal("recovery did not leave user lock", ctx.Err())
			}
			return
		}
	}
}
