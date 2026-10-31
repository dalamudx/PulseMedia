package httpapi_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestLoginRevalidatesLockedCurrentAccount(t *testing.T) {
	for _, change := range []string{"disabled", "password", "role"} {
		t.Run(change, func(t *testing.T) {
			a, svc, _ := setupAuthHTTP(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			user, _, err := svc.SetupAdmin(ctx, "login-fence", "LoginFenceFixture123!")
			if err != nil {
				t.Fatal(err)
			}
			tx, err := a.DBPool().Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			query := map[string]string{
				"disabled": "UPDATE app_user SET is_active=false WHERE id=$1",
				"password": "UPDATE app_user SET password_hash='changed-canonical-password' WHERE id=$1",
				"role":     "UPDATE app_user SET role='member' WHERE id=$1",
			}[change]
			if _, err = tx.Exec(ctx, query, user.ID); err != nil {
				t.Fatal(err)
			}
			type result struct {
				user  *ports.User
				token string
				err   error
			}
			done := make(chan result, 1)
			go func() { u, s, e := svc.Login(ctx, "login-fence", "LoginFenceFixture123!"); done <- result{u, s, e} }()
			joined := false
			defer func() {
				cancel()
				_ = tx.Rollback(context.Background())
				if !joined {
					<-done
				}
			}()
			// The initial MVCC read sees the old account, but issuance must wait for
			// our update. Scope observation to this disposable database only.
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				err = a.DBPool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '-- name: LockAccountForSecurity%')`).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case r := <-done:
					joined = true
					t.Fatalf("login issued before current account lock: token=%t error=%v", r.token != "", r.err)
				case <-ctx.Done():
					t.Fatal("login never reached account fence")
				case <-ticker.C:
				}
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			r := <-done
			joined = true
			if change == "role" {
				if r.err != nil || r.user == nil || r.user.Role != ports.RoleMember || r.token == "" {
					t.Fatalf("login retained stale role: %v", r.err)
				}
			} else {
				want := ports.ErrInvalidCredentials
				if change == "disabled" {
					want = ports.ErrUserDisabled
				}
				if !errors.Is(r.err, want) || r.token != "" || r.user != nil {
					t.Fatalf("changed account issued session: %v", r.err)
				}
			}
			var count int
			if err = a.DBPool().QueryRow(ctx, "SELECT count(*) FROM app_session WHERE user_id=$1", user.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if change == "role" {
				want = 2
			}
			if count != want {
				t.Fatalf("persisted sessions=%d want=%d", count, want)
			}
		})
	}
}
