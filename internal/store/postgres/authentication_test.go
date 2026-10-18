package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type authTestSecrets struct{ afterVerify func() }

func (authTestSecrets) HashPassword(p string) (string, error) { return crypto.HashPassword(p) }
func (s authTestSecrets) VerifyPassword(p, h string) bool {
	valid := crypto.VerifyPassword(p, h)
	if valid && s.afterVerify != nil {
		s.afterVerify()
	}
	return valid
}
func (authTestSecrets) GenerateToken(p string) (string, []byte, error) {
	return crypto.GenerateToken(p)
}
func (authTestSecrets) HashToken(p string) []byte { return crypto.HashToken(p) }

func newAuthenticationFixture(t *testing.T) (*pgxpool.Pool, *postgres.AuthenticationStore, *clock.MockClock) {
	t.Helper()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	ctx := t.Context()
	if _, err := postgres.Migrate(ctx, dsn, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, postgres.NewAuthenticationStore(pool), clock.NewMock(time.Now().UTC())
}

func TestAuthenticationSetupRollbackPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	svc := auth.NewService(repo, authTestSecrets{}, c, idgen.New())
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_auth_session() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic session fault'; END $$; CREATE TRIGGER reject_auth_session BEFORE INSERT ON app_session FOR EACH ROW EXECUTE FUNCTION reject_auth_session()`)
	if err != nil {
		t.Fatal(err)
	}
	u, plain, err := svc.SetupAdmin(ctx, "Admin", "Password123!")
	if !errors.Is(err, ports.ErrUnavailable) || u != nil || plain != "" {
		t.Fatalf("faulted setup returned identity: %v %q %v", u, plain, err)
	}
	for _, table := range []string{"app_user", "app_session"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial setup %s: %d %v", table, count, err)
		}
	}
	if _, err := pool.Exec(ctx, "DROP TRIGGER reject_auth_session ON app_session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SetupAdmin(ctx, "Admin", "Password123!"); err != nil {
		t.Fatal("rollback did not release setup boundary", err)
	}
}

func TestAuthenticationLoginRevalidatesCurrentAccountPG(t *testing.T) {
	for _, change := range []struct {
		name, sql string
		want      error
	}{
		{"disabled", "UPDATE app_user SET is_active=false WHERE id=$1", ports.ErrUserDisabled},
		{"password", "UPDATE app_user SET password_hash='changed' WHERE id=$1", ports.ErrInvalidCredentials},
		{"role", "UPDATE app_user SET role='member' WHERE id=$1", nil},
	} {
		t.Run(change.name, func(t *testing.T) {
			pool, repo, c := newAuthenticationFixture(t)
			ctx := t.Context()
			svc := auth.NewService(repo, authTestSecrets{}, c, idgen.New())
			user, _, err := svc.SetupAdmin(ctx, "Admin", "Password123!")
			if err != nil {
				t.Fatal(err)
			}
			secrets := authTestSecrets{afterVerify: func() {
				if _, err := pool.Exec(ctx, change.sql, user.ID); err != nil {
					t.Fatal(err)
				}
			}}
			login := auth.NewService(repo, secrets, c, idgen.New())
			current, plain, err := login.Login(ctx, "admin", "Password123!")
			if !errors.Is(err, change.want) {
				t.Fatalf("login=%v want %v", err, change.want)
			}
			var count int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM app_session").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if change.want != nil {
				if current != nil || plain != "" || count != 1 {
					t.Fatal("stale account issued a session")
				}
			} else if current == nil || current.Role != ports.RoleMember || plain == "" || count != 2 {
				t.Fatal("login did not use current role")
			}
		})
	}
}

func TestAuthenticationLoginWaitsForAccountLockPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	svc := auth.NewService(repo, authTestSecrets{}, c, idgen.New())
	user, _, err := svc.SetupAdmin(ctx, "Admin", "Password123!")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	called := false
	go func() {
		done <- repo.WithLoginAccount(ctx, user.ID, func(account ports.AuthenticationAccount, _ ports.AuthenticationSessionWriter) error {
			called = true
			if account.User.Role != ports.RoleMember {
				return errors.New("stale account role after lock wait")
			}
			return nil
		})
	}()
	// Always join the operation, including assertion failures and timeout paths.
	joined := false
	defer func() {
		if !joined {
			cancel()
			<-done
		}
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockUserForLogin%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if !waiting {
			select {
			case err := <-done:
				joined = true
				t.Fatalf("login returned before row lock release: %v", err)
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("login did not reach row lock")
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = <-done
	joined = true
	if err != nil || !called {
		t.Fatal("locked callback", called, err)
	}
}

func TestAuthenticationCredentialBoundariesPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	svc := auth.NewService(repo, authTestSecrets{}, c, idgen.New())
	user, plain, err := svc.SetupAdmin(ctx, "Admin", "Password123!")
	if err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	var expires, lastSeen time.Time
	var hash, csrf []byte
	if err := pool.QueryRow(ctx, "SELECT id,token_hash,csrf_token_hash,expires_at,last_seen_at FROM app_session").Scan(&id, &hash, &csrf, &expires, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if string(hash) == plain || len(hash) != 32 || len(csrf) != 32 || string(hash) != string(crypto.HashToken(plain)) {
		t.Fatal("session hash contract changed")
	}
	c.Advance(expires.Sub(c.Now()))
	if _, err := svc.AuthenticateToken(ctx, plain); !errors.Is(err, ports.ErrTokenExpired) {
		t.Fatalf("equal clock expiry accepted: %v", err)
	}
	var after time.Time
	if err := pool.QueryRow(ctx, "SELECT last_seen_at FROM app_session WHERE id=$1", id).Scan(&after); err != nil || !after.Equal(lastSeen) {
		t.Fatal("expired authentication touched session", err)
	}
	// Database-time filtering independently rejects a session expired in storage.
	if _, err := pool.Exec(ctx, "UPDATE app_session SET expires_at=statement_timestamp() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindSession(ctx, hash); !errors.Is(err, ports.ErrAuthenticationNotFound) {
		t.Fatalf("DB-expired session accepted: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'read',$3,'[\"read\"]')", uuid.New(), user.ID, crypto.HashToken("api-test")); err != nil {
		t.Fatal(err)
	}
	actor, err := svc.AuthenticateToken(ctx, "api-test")
	if err != nil || actor.Type != ports.ActorTypeUser || actor.Scopes == nil || len(actor.Scopes) != 1 || actor.Scopes[0] != "read" {
		t.Fatalf("read actor %v %v", actor, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE api_token SET scopes='null'"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateToken(ctx, "api-test"); !errors.Is(err, ports.ErrUnauthorized) {
		t.Fatal("null scopes accepted", err)
	}
	// API expiry also uses the injected clock, independently of the DB filter.
	if _, err := pool.Exec(ctx, "UPDATE api_token SET scopes='[\"read\"]', expires_at=$1", c.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateToken(ctx, "api-test"); !errors.Is(err, ports.ErrTokenExpired) {
		t.Fatal("equal clock API expiry accepted", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE api_token SET expires_at=NULL, revoked_at=statement_timestamp()"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateToken(ctx, "api-test"); !errors.Is(err, ports.ErrUnauthorized) {
		t.Fatal("revoked API token accepted", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE api_token SET revoked_at=NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateToken(ctx, "api-test"); !errors.Is(err, ports.ErrUnauthorized) {
		t.Fatal("disabled account authenticated", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE app_user SET is_active=true WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE api_token SET scopes='[\"read\"]', expires_at=statement_timestamp()"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindAPIToken(ctx, crypto.HashToken("api-test")); !errors.Is(err, ports.ErrAuthenticationNotFound) {
		t.Fatal("DB-expired API token accepted", err)
	}
}

func TestAuthenticationLoginRollbackPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	svc := auth.NewService(repo, authTestSecrets{}, c, idgen.New())
	user, _, err := svc.SetupAdmin(ctx, "Admin", "Password123!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_login_session() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic login fault'; END $$; CREATE TRIGGER reject_login_session BEFORE INSERT ON app_session FOR EACH ROW EXECUTE FUNCTION reject_login_session()`); err != nil {
		t.Fatal(err)
	}
	if u, p, err := svc.Login(ctx, "Admin", "Password123!"); !errors.Is(err, ports.ErrUnavailable) || u != nil || p != "" {
		t.Fatal("faulted login returned a credential", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM app_session WHERE user_id=$1", user.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("faulted login changed sessions", count, err)
	}
	if _, err := pool.Exec(ctx, "DROP TRIGGER reject_login_session ON app_session"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "Admin", "Password123!"); err != nil {
		t.Fatal("rollback did not release account lock", err)
	}
}

func TestAuthenticationMalformedPasswordAndLogoutFaultPG(t *testing.T) {
	pool, repo, c := newAuthenticationFixture(t)
	ctx := t.Context()
	svc := auth.NewService(repo, authTestSecrets{}, c, idgen.New())
	_, plain, err := svc.SetupAdmin(ctx, "Admin", "Password123!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE app_user SET password_hash='$argon2id$v=19$m=999999999,t=1,p=4$bad$bad'"); err != nil {
		t.Fatal(err)
	}
	if u, p, err := svc.Login(ctx, "Admin", "Password123!"); !errors.Is(err, ports.ErrInvalidCredentials) || u != nil || p != "" {
		t.Fatal("malformed hash accepted", err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_auth_logout() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic logout fault'; END $$; CREATE TRIGGER reject_auth_logout BEFORE DELETE ON app_session FOR EACH ROW EXECUTE FUNCTION reject_auth_logout()`); err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, plain); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("logout fault hidden", err)
	}
	if _, err := svc.AuthenticateToken(ctx, plain); err != nil {
		t.Fatal("failed logout removed session", err)
	}
	pool.Close()
	if initialized, err := svc.IsInitialized(ctx); !errors.Is(err, ports.ErrUnavailable) || initialized {
		t.Fatal("failed initialization read hidden", err)
	}
	if err := svc.Logout(ctx, plain); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("failed logout lookup hidden", err)
	}
}
