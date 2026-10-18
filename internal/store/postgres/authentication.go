package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const authenticationSetupLockID = 0x50756c736553

type AuthenticationStore struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func NewAuthenticationStore(pool *pgxpool.Pool) *AuthenticationStore {
	return &AuthenticationStore{pool: pool, queries: db.New(pool)}
}

var _ ports.AuthenticationRepository = (*AuthenticationStore)(nil)

type authenticationSetup struct{ queries *db.Queries }
type authenticationSessions struct{ queries *db.Queries }

func authenticationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrAuthenticationNotFound
	}
	return ports.ErrUnavailable
}

func authenticationUser(u db.AppUser) ports.User {
	return ports.User{ID: u.ID, Username: u.Username, Role: u.Role, IsActive: u.IsActive, CreatedAt: u.CreatedAt.Time, UpdatedAt: u.UpdatedAt.Time}
}

func (s *AuthenticationStore) IsInitialized(ctx context.Context) (bool, error) {
	count, err := s.queries.CountUsers(ctx)
	return count > 0, authenticationError(err)
}

func (s *AuthenticationStore) WithAdminSetup(ctx context.Context, fn func(ports.AuthenticationSetup) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return authenticationError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", authenticationSetupLockID); err != nil {
		return authenticationError(err)
	}
	if err := fn(&authenticationSetup{queries: db.New(tx)}); err != nil {
		return err
	}
	return authenticationError(tx.Commit(ctx))
}

func (t *authenticationSetup) IsInitialized(ctx context.Context) (bool, error) {
	count, err := t.queries.CountUsers(ctx)
	return count > 0, authenticationError(err)
}

func (t *authenticationSetup) CreateAdmin(ctx context.Context, id uuid.UUID, username, passwordHash string) (ports.User, error) {
	u, err := t.queries.CreateUser(ctx, db.CreateUserParams{ID: id, Username: username, PasswordHash: passwordHash, Role: ports.RoleAdmin, IsActive: true})
	return authenticationUser(u), authenticationError(err)
}

func (t *authenticationSetup) CreateSession(ctx context.Context, session ports.Session) error {
	return (&authenticationSessions{queries: t.queries}).CreateSession(ctx, session)
}

func (t *authenticationSessions) CreateSession(ctx context.Context, session ports.Session) error {
	_, err := t.queries.CreateSession(ctx, db.CreateSessionParams{
		ID: session.ID, UserID: session.UserID, TokenHash: session.TokenHash, CsrfTokenHash: session.CsrfTokenHash,
		ExpiresAt: pgtype.Timestamptz{Time: session.ExpiresAt, Valid: true},
	})
	return authenticationError(err)
}

func (s *AuthenticationStore) FindAccount(ctx context.Context, username string) (ports.AuthenticationAccount, error) {
	u, err := s.queries.GetUserByUsername(ctx, username)
	return ports.AuthenticationAccount{User: authenticationUser(u), PasswordHash: u.PasswordHash}, authenticationError(err)
}

func (s *AuthenticationStore) WithLoginAccount(ctx context.Context, id uuid.UUID, fn func(ports.AuthenticationAccount, ports.AuthenticationSessionWriter) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return authenticationError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	u, err := q.LockUserForLogin(ctx, id)
	if err != nil {
		return authenticationError(err)
	}
	if err := fn(ports.AuthenticationAccount{User: authenticationUser(u), PasswordHash: u.PasswordHash}, &authenticationSessions{queries: q}); err != nil {
		return err
	}
	return authenticationError(tx.Commit(ctx))
}

func (s *AuthenticationStore) FindSession(ctx context.Context, hash []byte) (ports.AuthenticationSession, error) {
	r, err := s.queries.GetSessionByTokenHash(ctx, hash)
	return ports.AuthenticationSession{
		ID: r.ID, User: ports.User{ID: r.UserID, Username: r.Username, Role: r.Role, IsActive: r.IsActive}, ExpiresAt: r.ExpiresAt.Time,
	}, authenticationError(err)
}

func (s *AuthenticationStore) FindAPIToken(ctx context.Context, hash []byte) (ports.AuthenticationToken, error) {
	r, err := s.queries.GetAPITokenByHash(ctx, hash)
	var expires, revoked *time.Time
	if r.ExpiresAt.Valid {
		expires = &r.ExpiresAt.Time
	}
	if r.RevokedAt.Valid {
		revoked = &r.RevokedAt.Time
	}
	return ports.AuthenticationToken{
		User: ports.User{ID: r.UserID, Username: r.Username, Role: r.Role, IsActive: r.IsActive}, ScopesJSON: r.Scopes, ExpiresAt: expires, RevokedAt: revoked,
	}, authenticationError(err)
}

func (s *AuthenticationStore) TouchSession(ctx context.Context, id uuid.UUID) error {
	return authenticationError(s.queries.TouchSession(ctx, id))
}

func (s *AuthenticationStore) DeleteSession(ctx context.Context, id uuid.UUID) error {
	return authenticationError(s.queries.DeleteSession(ctx, id))
}
