package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

const migrationsDir = "../../../migrations"

// Test assembly uses the same canonical crypto functions as the application.
type authenticationSecrets struct{}

func (authenticationSecrets) HashPassword(p string) (string, error) { return crypto.HashPassword(p) }
func (authenticationSecrets) VerifyPassword(p, h string) bool       { return crypto.VerifyPassword(p, h) }
func (authenticationSecrets) GenerateToken(p string) (string, []byte, error) {
	return crypto.GenerateToken(p)
}
func (authenticationSecrets) HashToken(p string) []byte { return crypto.HashToken(p) }

func setupAuthService(t *testing.T) (*auth.Service, *clock.MockClock, *idgen.UUIDv7Generator) {
	t.Helper()
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsDir); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	pool, err := postgres.NewPool(ctx, dbDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
	})

	mockClock := clock.NewMock(time.Now().UTC())
	gen := idgen.New()
	svc := auth.NewService(postgres.NewAuthenticationStore(pool), authenticationSecrets{}, mockClock, gen)
	return svc, mockClock, gen
}

func TestConcurrentAdminSetup(t *testing.T) {
	svc, _, _ := setupAuthService(t)
	ctx := context.Background()

	// Initially not initialized
	init, err := svc.IsInitialized(ctx)
	if err != nil || init {
		t.Fatalf("expected uninitialized initially: init=%v, err=%v", init, err)
	}

	// 5 concurrent attempts to set up admin
	const concurrency = 5
	var wg sync.WaitGroup
	results := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _, err := svc.SetupAdmin(ctx, "Administrator", "SuperSecretPass123!")
			results[idx] = err
		}(i)
	}

	wg.Wait()

	// Exactly ONE attempt must succeed, all others must return ErrAlreadyInitialized
	successCount := 0
	alreadyInitCount := 0
	for _, res := range results {
		if res == nil {
			successCount++
		} else if errors.Is(res, ports.ErrAlreadyInitialized) {
			alreadyInitCount++
		} else {
			t.Fatalf("unexpected error during concurrent setup: %v", res)
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly 1 success during concurrent setup, got: %d", successCount)
	}
	if alreadyInitCount != concurrency-1 {
		t.Fatalf("expected %d ErrAlreadyInitialized, got: %d", concurrency-1, alreadyInitCount)
	}

	// Verify initialized now true
	init, err = svc.IsInitialized(ctx)
	if err != nil || !init {
		t.Fatalf("expected initialized=true after setup, got: %v", init)
	}

	// Subsequent setup attempt must fail (closed entry point)
	_, _, err = svc.SetupAdmin(ctx, "AnotherAdmin", "AnotherPassword123!")
	if !errors.Is(err, ports.ErrAlreadyInitialized) {
		t.Fatalf("expected ErrAlreadyInitialized on subsequent setup, got: %v", err)
	}
}

func TestLoginAndSessionValidation(t *testing.T) {
	svc, mockClock, _ := setupAuthService(t)
	ctx := context.Background()

	user, token, err := svc.SetupAdmin(ctx, "AdminUser", "CorrectPassword123!")
	if err != nil {
		t.Fatalf("setup admin failed: %v", err)
	}
	if user.Role != ports.RoleAdmin {
		t.Fatalf("expected admin role, got: %s", user.Role)
	}

	// 1. Authenticate with valid token
	actor, err := svc.AuthenticateToken(ctx, token)
	if err != nil {
		t.Fatalf("expected token authentication to succeed, got: %v", err)
	}
	if actor.ID != user.ID.String() || actor.Role != ports.RoleAdmin {
		t.Fatalf("unexpected actor: %+v", actor)
	}

	// Token actor can NEVER have ActorTypeSystem
	if actor.Type == ports.ActorTypeSystem {
		t.Fatal("token actor must not have system type")
	}

	// 2. Login with wrong password fails
	_, _, err = svc.Login(ctx, "AdminUser", "WrongPassword123!")
	if !errors.Is(err, ports.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got: %v", err)
	}

	// 3. Login with correct password succeeds
	loginUser, loginToken, err := svc.Login(ctx, "adminuser", "CorrectPassword123!")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if loginUser.ID != user.ID {
		t.Fatalf("user ID mismatch on login")
	}

	// 4. Token expiration
	mockClock.Advance(8 * 24 * time.Hour) // beyond 7 days
	_, err = svc.AuthenticateToken(ctx, loginToken)
	if !errors.Is(err, ports.ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired after 8 days, got: %v", err)
	}

	// 5. Logout deletes session
	svc2, _, _ := setupAuthService(t)
	_, tok2, err := svc2.SetupAdmin(ctx, "Admin2", "Password12345!")
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	if err := svc2.Logout(ctx, tok2); err != nil {
		t.Fatalf("logout failed: %v", err)
	}

	_, err = svc2.AuthenticateToken(ctx, tok2)
	if !errors.Is(err, ports.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized after logout, got: %v", err)
	}
}
