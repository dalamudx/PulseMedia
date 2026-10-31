package postgres

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/mediarequest"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"strconv"
)

type SeerrStore struct {
	pool     *pgxpool.Pool
	requests *MediaRequestStore
	secrets  ports.SecretCipher
}

func NewSeerrStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock, secrets ports.SecretCipher) *SeerrStore {
	return &SeerrStore{pool: pool, requests: NewMediaRequestStore(pool, ids, clock), secrets: secrets}
}

var _ ports.SeerrRepository = (*SeerrStore)(nil)

type seerrConfig struct {
	source, owner, admin uuid.UUID
	enabled              bool
	version              int64
	cipher               []byte
}

func seerrLock(ctx context.Context, tx pgx.Tx) error {
	return db.New(tx).LockSeerrConfigurationCommand(ctx)
}
func seerrLoad(ctx context.Context, tx pgx.Tx) (c seerrConfig, e error) {
	row, e := db.New(tx).LockSeerrConfiguration(ctx)
	return seerrConfig{source: row.SourceID, owner: row.OwnerID, admin: row.ConfiguredBy, enabled: row.Enabled, version: row.Version, cipher: row.CredentialEncrypted}, e
}
func seerrAdmin(ctx context.Context, q *db.Queries, a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return lockAcquisitionUser(ctx, q, a)
}
func seerrDTO(c seerrConfig) ports.SeerrConfiguration {
	r := ports.SeerrConfiguration{Enabled: c.enabled, Version: c.version, CredentialPresent: len(c.cipher) > 0}
	if c.owner != uuid.Nil {
		r.OwnerID = &c.owner
	}
	return r
}
func (s *SeerrStore) ReadSeerrConfiguration(ctx context.Context, a *ports.Actor) (out ports.SeerrConfiguration, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer creationRollback(ctx, tx)
	if e = seerrLock(ctx, tx); e != nil {
		return out, e
	}
	if e = seerrAdmin(ctx, db.New(tx), a); e != nil {
		return out, e
	}
	c, e := seerrLoad(ctx, tx)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, nil
	}
	return seerrDTO(c), e
}
func (s *SeerrStore) SaveSeerrConfiguration(ctx context.Context, a *ports.Actor, in ports.SeerrConfigurationInput) (out ports.SeerrConfiguration, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer creationRollback(ctx, tx)
	q := db.New(tx)
	if e = seerrLock(ctx, tx); e != nil {
		return out, e
	}
	if e = seerrAdmin(ctx, q, a); e != nil {
		return out, e
	}
	if e = mediaRequestIsolation(ctx, q); e != nil {
		return out, e
	}
	c, e := seerrLoad(ctx, tx)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	if c.version != in.ExpectedVersion || c.owner != uuid.Nil && c.owner != in.OwnerID {
		return out, ports.ErrMediaRequestConflict
	}
	if in.OwnerID == uuid.Nil || in.ExpectedVersion < 0 {
		return out, ports.ErrRejected
	}
	owner, e := q.LockAcquisitionCommandUser(ctx, in.OwnerID)
	if e != nil || !owner.IsActive || (owner.Role != ports.RoleMember && owner.Role != ports.RoleAdmin) {
		return out, ports.ErrForbidden
	}
	if in.Credential != "" {
		if !seerrCredential(in.Credential) {
			return out, ports.ErrRejected
		}
		if s.secrets == nil {
			return out, ports.ErrUnavailable
		}
		c.cipher, e = s.secrets.EncryptSecret([]byte(in.Credential))
		if e != nil {
			return out, ports.ErrUnavailable
		}
	}
	if len(c.cipher) == 0 {
		return out, ports.ErrRejected
	}
	if s.requests.ids == nil {
		return out, ports.ErrUnavailable
	}
	if c.version == 0 {
		c.source = s.requests.ids.NewUUID()
		if c.source == uuid.Nil {
			return out, ports.ErrUnavailable
		}
	}
	c.owner = in.OwnerID
	c.admin = uuid.MustParse(a.ID)
	c.enabled = in.Enabled
	c.version++
	e = q.SaveSeerrConfiguration(ctx, db.SaveSeerrConfigurationParams{SourceID: c.source, Enabled: c.enabled, Version: c.version, OwnerID: c.owner, ConfiguredBy: c.admin, CredentialEncrypted: c.cipher})
	if e != nil {
		return out, e
	}
	out = seerrDTO(c)
	raw, _ := json.Marshal(out)
	_, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.requests.ids.NewUUID(), ActorType: "user", ActorID: a.ID, Action: "seerr.configuration.saved", TargetType: "seerr", TargetID: c.source.String(), Payload: raw})
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func seerrCredential(v string) bool {
	if len(v) < 32 || len(v) > 256 {
		return false
	}
	for _, r := range v {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
func (s *SeerrStore) ReceiveSeerr(ctx context.Context, credential string, event ports.SeerrEvent) (out ports.SeerrReceipt, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	if !seerrCredential(credential) {
		return out, ports.ErrUnauthorized
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer creationRollback(ctx, tx)
	q := db.New(tx)
	if e = seerrLock(ctx, tx); e != nil {
		return out, e
	}
	c, e := seerrLoad(ctx, tx)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ports.ErrUnauthorized
	}
	if e != nil {
		return out, e
	}
	if !c.enabled {
		return out, ports.ErrUnauthorized
	}
	if s.secrets == nil {
		return out, ports.ErrUnavailable
	}
	plain, e := s.secrets.DecryptSecret(c.cipher)
	if e != nil {
		return out, ports.ErrUnavailable
	}
	valid := subtle.ConstantTimeCompare(plain, []byte(credential)) == 1
	clear(plain)
	if !valid {
		return out, ports.ErrUnauthorized
	}
	// Resolve authority from locked database accounts, never from inbound requester fields.
	admin, e := q.LockAcquisitionCommandUser(ctx, c.admin)
	if e != nil || !admin.IsActive || admin.Role != ports.RoleAdmin {
		return out, ports.ErrForbidden
	}
	owner, e := q.LockAcquisitionCommandUser(ctx, c.owner)
	if e != nil || !owner.IsActive || (owner.Role != ports.RoleMember && owner.Role != ports.RoleAdmin) {
		return out, ports.ErrForbidden
	}
	canonical, e := mediarequest.CanonicalIntent(event.Intent)
	if e != nil || !creationHash(event.PayloadHash) {
		return out, ports.ErrRejected
	}
	id, e := strconv.ParseInt(event.ExternalRequestID, 10, 64)
	if e != nil || id <= 0 || strconv.FormatInt(id, 10) != event.ExternalRequestID {
		return out, ports.ErrRejected
	}
	old, e := q.GetSeerrIngressReceipt(ctx, db.GetSeerrIngressReceiptParams{SourceID: c.source, ExternalRequestID: event.ExternalRequestID})
	if e == nil {
		if old.PayloadHash != event.PayloadHash || old.OwnerID != c.owner {
			return out, ports.ErrMediaRequestConflict
		}
		out = ports.SeerrReceipt{ExternalRequestID: event.ExternalRequestID, RequestID: old.RequestID, ConfigVersion: old.ConfigVersion, CreatedAt: old.CreatedAt.Time.UTC(), State: "pending"}
		return out, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: c.owner.String(), Role: owner.Role}
	key := "seerr:" + creationDigest([]string{c.source.String(), event.ExternalRequestID})
	receipt, e := s.requests.submitInTransaction(ctx, q, actor, canonical, key)
	if e != nil {
		return out, e
	}
	out = ports.SeerrReceipt{ExternalRequestID: event.ExternalRequestID, RequestID: receipt.Request.ID, ConfigVersion: c.version, State: "pending", CreatedAt: receipt.Request.CreatedAt}
	e = q.InsertSeerrIngressReceipt(ctx, db.InsertSeerrIngressReceiptParams{SourceID: c.source, ExternalRequestID: event.ExternalRequestID, PayloadHash: event.PayloadHash, ConfigVersion: c.version, OwnerID: c.owner, RequestID: out.RequestID, CreatedAt: pgtype.Timestamptz{Time: out.CreatedAt, Valid: true}})
	if e != nil {
		return out, e
	}
	raw, _ := json.Marshal(out)
	_, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.requests.ids.NewUUID(), ActorType: "service", ActorID: "seerr", Action: "seerr.request.received", TargetType: "media_request", TargetID: out.RequestID.String(), Payload: raw})
	if e != nil {
		return out, e
	}
	if e = mediaRequestIsolation(ctx, q); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *SeerrStore) ListSeerrReceipts(ctx context.Context, a *ports.Actor) (out []ports.SeerrReceipt, err error) {
	defer func() { err = mediaRequestError(ctx, err) }()
	out = []ports.SeerrReceipt{}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer creationRollback(ctx, tx)
	if e = seerrAdmin(ctx, db.New(tx), a); e != nil {
		return nil, e
	}
	rows, e := db.New(tx).ListSeerrIngressReceipts(ctx)
	if e != nil {
		return nil, e
	}
	for _, row := range rows {
		out = append(out, ports.SeerrReceipt{ExternalRequestID: row.ExternalRequestID, RequestID: row.RequestID, ConfigVersion: row.ConfigVersion, CreatedAt: row.CreatedAt.Time.UTC(), State: "pending"})
	}
	return out, nil
}
