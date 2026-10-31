package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AccountManagementStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewAccountManagementStore(pool *pgxpool.Pool, ids ports.IDGen) *AccountManagementStore {
	return &AccountManagementStore{pool, ids}
}

var _ ports.AccountManagementRepository = (*AccountManagementStore)(nil)

type accountManagementTx struct {
	*mfaTransaction
	account       ports.AuthenticationAccount
	session       uuid.UUID
	ids           ports.IDGen
	adminBoundary bool
}

func (t *accountManagementTx) Account() ports.AuthenticationAccount { return t.account }
func (t *accountManagementTx) SessionID() uuid.UUID                 { return t.session }
func securityStoreError(err error) error {
	if err == nil {
		return nil
	}
	for _, e := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrInvalidCredentials, ports.ErrInvalidAccountCommand, ports.ErrAccountCommandConflict, ports.ErrLastActiveAdmin, ports.ErrUnavailable} {
		if errors.Is(err, e) {
			return e
		}
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ports.ErrAccountCommandConflict
	}
	return ports.ErrUnavailable
}
func (s *AccountManagementStore) WithAccountManagement(ctx context.Context, identity ports.SecurityIdentity, admin bool, fn func(ports.AccountManagementTx) error) (err error) {
	owner, e := tokenOwner(identity.Actor)
	if e != nil {
		return e
	}
	if len(identity.SessionHash) != 32 {
		return ports.ErrUnauthorized
	}
	defer func() { err = securityStoreError(err) }()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if admin {
		// One ordering for actor/target locks prevents reciprocal administrator edits.
		if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x50756c734143)); e != nil {
			return e
		}
		if _, e = q.LockAllAccountsForManagement(ctx); e != nil {
			return e
		}
	}
	u, e := q.LockAccountForSecurity(ctx, owner)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrForbidden
	}
	if e != nil {
		return e
	}
	if !u.IsActive || (u.Role != ports.RoleAdmin && u.Role != ports.RoleMember) || (admin && u.Role != ports.RoleAdmin) {
		return ports.ErrForbidden
	}
	session, e := q.LockSecuritySession(ctx, db.LockSecuritySessionParams{UserID: owner, TokenHash: identity.SessionHash})
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrUnauthorized
	}
	if e != nil {
		return e
	}
	t := &accountManagementTx{mfaTransaction: &mfaTransaction{tx, owner}, account: ports.AuthenticationAccount{User: authenticationUser(u), PasswordHash: u.PasswordHash}, session: session, ids: s.ids, adminBoundary: admin}
	if e = fn(t); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (t *accountManagementTx) ListAccounts(ctx context.Context) ([]ports.User, error) {
	if !t.adminBoundary || t.account.User.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	rows, e := db.New(t.tx).ListManagedAccounts(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]ports.User, 0, len(rows))
	for _, u := range rows {
		out = append(out, authenticationUser(u))
	}
	return out, nil
}
func (t *accountManagementTx) CreateAccount(ctx context.Context, id uuid.UUID, name, hash, role string) (ports.User, error) {
	if !t.adminBoundary || t.account.User.Role != ports.RoleAdmin {
		return ports.User{}, ports.ErrForbidden
	}
	u, e := db.New(t.tx).CreateUser(ctx, db.CreateUserParams{ID: id, Username: name, PasswordHash: hash, Role: role, IsActive: true})
	return authenticationUser(u), e
}
func (t *accountManagementTx) UpdateAccount(ctx context.Context, id uuid.UUID, c ports.AccountUpdate) (ports.User, error) {
	if !t.adminBoundary || t.account.User.Role != ports.RoleAdmin {
		return ports.User{}, ports.ErrForbidden
	}
	q := db.New(t.tx)
	old, e := q.LockAccountForSecurity(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.User{}, ports.ErrNotFound
	}
	if e != nil {
		return ports.User{}, e
	}
	if old.IsActive && old.Role == ports.RoleAdmin && (!c.IsActive || c.Role != ports.RoleAdmin) {
		count, e := q.CountActiveAdministrators(ctx)
		if e != nil {
			return ports.User{}, e
		}
		if count <= 1 {
			return ports.User{}, ports.ErrLastActiveAdmin
		}
	}
	u, e := q.UpdateManagedAccount(ctx, db.UpdateManagedAccountParams{ID: id, Role: c.Role, IsActive: c.IsActive})
	if e != nil {
		return ports.User{}, e
	}
	if old.Role != u.Role || old.IsActive != u.IsActive {
		if e = t.revokeCredentials(ctx, id, uuid.Nil); e != nil {
			return ports.User{}, e
		}
	}
	return authenticationUser(u), nil
}
func (t *accountManagementTx) ListSessions(ctx context.Context) ([]ports.ManagedSession, error) {
	rows, e := db.New(t.tx).ListManagedSessions(ctx, t.user)
	if e != nil {
		return nil, e
	}
	out := make([]ports.ManagedSession, 0, len(rows))
	for _, r := range rows {
		out = append(out, ports.ManagedSession{ID: r.ID, CreatedAt: r.CreatedAt.Time, LastSeenAt: r.LastSeenAt.Time, ExpiresAt: r.ExpiresAt.Time, Current: r.ID == t.session})
	}
	return out, nil
}
func (t *accountManagementTx) RevokeSession(ctx context.Context, id uuid.UUID) error {
	count, e := db.New(t.tx).RevokeManagedSession(ctx, db.RevokeManagedSessionParams{ID: id, UserID: t.user})
	if e != nil {
		return e
	}
	if count != 1 {
		return ports.ErrNotFound
	}
	return nil
}
func (t *accountManagementTx) ChangePassword(ctx context.Context, hash string) error {
	return db.New(t.tx).ChangeManagedPassword(ctx, db.ChangeManagedPasswordParams{ID: t.user, PasswordHash: hash})
}
func (t *accountManagementTx) revokeCredentials(ctx context.Context, user, keep uuid.UUID) error {
	q := db.New(t.tx)
	if e := q.DeleteOtherManagedSessions(ctx, db.DeleteOtherManagedSessionsParams{UserID: user, KeepSessionID: keep}); e != nil {
		return e
	}
	return q.RevokeManagedAPITokens(ctx, user)
}
func (t *accountManagementTx) RevokeOtherCredentials(ctx context.Context) error {
	return t.revokeCredentials(ctx, t.user, t.session)
}
func (t *accountManagementTx) Receipt(ctx context.Context, key, fp string, out any) (bool, error) {
	if key == "" || len(key) > 160 || len(fp) != 64 {
		return false, ports.ErrInvalidAccountCommand
	}
	receipt, e := db.New(t.tx).ReadAccountSecurityCommand(ctx, db.ReadAccountSecurityCommandParams{OwnerID: t.user, CommandKey: key})
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if receipt.RequestDigest != fp {
		return false, ports.ErrAccountCommandConflict
	}
	if e = json.Unmarshal(receipt.Response, out); e != nil {
		return false, e
	}
	return true, nil
}
func (t *accountManagementTx) Finish(ctx context.Context, key, fp, action string, target uuid.UUID, response json.RawMessage) error {
	q := db.New(t.tx)
	if e := q.InsertAccountSecurityCommand(ctx, db.InsertAccountSecurityCommandParams{OwnerID: t.user, CommandKey: key, RequestDigest: fp, Action: action, TargetID: target, Response: response}); e != nil {
		return e
	}
	// Payload consists solely of the safe receipt; callers strip all first-view secrets.
	if _, e := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: t.ids.NewUUID(), ActorType: string(ports.ActorTypeUser), ActorID: t.user.String(), Action: action, TargetType: "account", TargetID: target.String(), Payload: response}); e != nil {
		return e
	}
	if e := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "account", AggregateID: target.String()}); e != nil {
		return e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "account", AggregateID: target.String()})
	if e != nil {
		return e
	}
	_, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: t.ids.NewUUID(), AggregateType: "account", AggregateID: target.String(), EventType: action, Seq: int64(seq), Payload: response})
	return e
}
