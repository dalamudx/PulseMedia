package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

func TestTransferLeaseCheckedAfterRowLockWait(t *testing.T) {
	for _, action := range []string{"guard", "finish", "review"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			dsn := pgtest.GetCluster(t).NewTestDB(t)
			if _, e := postgres.Migrate(ctx, dsn, "../../../migrations"); e != nil {
				t.Fatal(e)
			}
			p, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
			if e != nil {
				t.Fatal(e)
			}
			defer p.Close()
			q := db.New(p)
			root, id := uuid.New(), uuid.New()
			if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: "/configured/fixture", MediaType: "movie", Name: "fixture"}); e != nil {
				t.Fatal(e)
			}
			if _, e = q.CreateTransferPlan(ctx, db.CreateTransferPlanParams{ID: id, OperationID: uuid.New(), RootID: root, State: "approved", Strategy: "hardlink"}); e != nil {
				t.Fatal(e)
			}
			var deadline time.Time
			if e = p.QueryRow(ctx, "UPDATE transfer_plan SET state='executing',execution_owner='lease-owner',execution_token=1,lease_until=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING lease_until", id).Scan(&deadline); e != nil {
				t.Fatal(e)
			}
			block, e := p.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = block.Rollback(context.Background()) }()
			var locked uuid.UUID
			if e = block.QueryRow(ctx, "SELECT id FROM transfer_plan WHERE id=$1 FOR UPDATE", id).Scan(&locked); e != nil {
				t.Fatal(e)
			}
			conn, e := p.Acquire(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer conn.Release()
			if _, e = conn.Exec(ctx, "SET lock_timeout='5s'"); e != nil {
				t.Fatal(e)
			}
			pid := conn.Conn().PgConn().PID()
			done := make(chan error, 1)
			joined := false
			go func() {
				query := db.New(conn)
				owner := "lease-owner"
				var e error
				switch action {
				case "guard":
					_, e = query.GuardTransferPlan(ctx, db.GuardTransferPlanParams{ID: id, Owner: owner, Token: 1})
				case "finish":
					_, e = query.FinishTransferPlan(ctx, db.FinishTransferPlanParams{ID: id, Owner: owner, Token: 1})
				case "review":
					_, e = query.ReviewTransferPlan(ctx, db.ReviewTransferPlanParams{ID: id, Owner: owner, Token: 1})
				}
				done <- e
			}()
			defer func() {
				_ = block.Rollback(context.Background())
				if joined {
					return
				}
				select {
				case <-done:
				case <-ctx.Done():
				}
			}()
			for {
				var waiting bool
				var started time.Time
				if e = p.QueryRow(ctx, "SELECT COALESCE(wait_event_type='Lock',false),COALESCE(query_start,clock_timestamp()) FROM pg_stat_activity WHERE pid=$1", pid).Scan(&waiting, &started); e != nil {
					t.Fatal(e)
				}
				if waiting {
					if !started.Before(deadline) {
						t.Fatal("probe did not begin while lease valid")
					}
					break
				}
				select {
				case e := <-done:
					joined = true
					t.Fatal("probe never blocked", e)
				case <-ctx.Done():
					t.Fatal("row-lock probe deadline")
				case <-time.After(5 * time.Millisecond):
				}
			}
			timer := time.NewTimer(time.Until(deadline) + 50*time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal("clock probe deadline")
			}
			if e = block.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			select {
			case e = <-done:
				joined = true
				if !errors.Is(e, pgx.ErrNoRows) {
					t.Fatal("lease expired during row lock but accepted", action, e)
				}
			case <-ctx.Done():
				t.Fatal("guard did not finish")
			}
			var state string
			var until time.Time
			if e = p.QueryRow(ctx, "SELECT state,lease_until FROM transfer_plan WHERE id=$1", id).Scan(&state, &until); e != nil || state != "executing" || !until.Equal(deadline) {
				t.Fatal("expired owner rewrote state/lease", state, until, e)
			}
		})
	}
}
