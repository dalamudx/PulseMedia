package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SiteCheckinConfigStore struct {
	pool    *pgxpool.Pool
	ids     ports.IDGen
	decoder ports.CredentialDecoder
}

var _ ports.SiteCheckinConfigRepository = (*SiteCheckinConfigStore)(nil)

func NewSiteCheckinConfigStore(pool *pgxpool.Pool, ids ports.IDGen, decoder ports.CredentialDecoder) *SiteCheckinConfigStore {
	return &SiteCheckinConfigStore{pool, ids, decoder}
}
func checkinActor(actor *ports.Actor) (*ports.Actor, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	a := *actor
	if actor.Scopes != nil {
		a.Scopes = append([]string{}, actor.Scopes...)
	}
	if err := managementActor(&a); err != nil {
		return nil, err
	}
	if id, err := uuid.Parse(a.ID); err != nil || id.String() != a.ID {
		return nil, ports.ErrForbidden
	}
	return &a, nil
}
func checkinValid(in ports.SiteCheckinConfigInput) bool {
	return indexer.ValidSiteAccountID(in.ExpectedAccountID) && in.Profile == ports.NexusPHP1103CheckinProfile && indexer.ValidSiteCheckinSchedule(in.Timezone, in.DailyAt) && in.ExpectedVersion >= 0 && in.ExpectedVersion < math.MaxInt64 && in.ExpectedSourceVersion >= 1
}
func checkinHash(in ports.SiteCheckinConfigInput) string {
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *SiteCheckinConfigStore) begin(ctx context.Context, a *ports.Actor) (pgx.Tx, *db.Queries, error) {
	if s == nil || s.pool == nil {
		return nil, nil, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, nil, err
	}
	q := db.New(tx)
	if err = statsGuard(ctx, q, a); err != nil {
		managementRollback(ctx, tx)
		return nil, nil, err
	}
	return tx, q, nil
}
func checkinConfig(row db.SiteCheckinConfig) ports.SiteCheckinConfig {
	return ports.SiteCheckinConfig{IndexerID: row.IndexerID, ExpectedAccountID: row.ExpectedAccountID, Profile: row.Profile, Enabled: row.Enabled, Timezone: row.Timezone, DailyAt: row.DailyAt, Version: row.Version, SourceConfigVersion: row.SourceConfigVersion, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
}

// Compare instants before normalizing only local copies; return the original JSON timestamps.
func checkinSame(a, b ports.SiteCheckinConfig) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	a.CreatedAt = b.CreatedAt
	a.UpdatedAt = b.UpdatedAt
	return a == b
}
func checkinVerified(row db.SiteCheckinConfigReceipt) (*ports.SiteCheckinConfigReceipt, error) {
	var in ports.SiteCheckinConfigInput
	var c ports.SiteCheckinConfig
	if checkinDecode(row.Input, &in) != nil || checkinDecode(row.Snapshot, &c) != nil || !checkinValid(in) || checkinHash(in) != row.RequestHash || row.OwnerID == uuid.Nil || row.IndexerID == uuid.Nil || !doubanSyncASCII(row.CommandKey, 160, false) || c.IndexerID != row.IndexerID || c.Version != row.Version || c.Version != in.ExpectedVersion+1 || c.SourceConfigVersion != in.ExpectedSourceVersion || c.ExpectedAccountID != in.ExpectedAccountID || c.Profile != in.Profile || c.Enabled != in.Enabled || c.Timezone != in.Timezone || c.DailyAt != in.DailyAt || c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) {
		return nil, ports.ErrEndpointVersionConflict
	}
	return &ports.SiteCheckinConfigReceipt{CommandKey: row.CommandKey, Config: c}, nil
}
func checkinSnapshot(ctx context.Context, q *db.Queries, row db.SiteCheckinConfig) (*ports.SiteCheckinConfig, error) {
	receipt, err := q.GetSiteCheckinConfigReceiptVersion(ctx, db.GetSiteCheckinConfigReceiptVersionParams{IndexerID: row.IndexerID, Version: row.Version})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	r, err := checkinVerified(receipt)
	if err != nil {
		return nil, err
	}
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid || !checkinSame(r.Config, checkinConfig(row)) {
		return nil, ports.ErrEndpointVersionConflict
	}
	return &r.Config, nil
}
func checkinReceipt(ctx context.Context, q *db.Queries, owner, id uuid.UUID, key string) (*ports.SiteCheckinConfigReceipt, string, error) {
	row, err := q.GetSiteCheckinConfigReceipt(ctx, db.GetSiteCheckinConfigReceiptParams{OwnerID: owner, IndexerID: id, CommandKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ports.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if row.OwnerID != owner || row.IndexerID != id || row.CommandKey != key {
		return nil, "", ports.ErrEndpointVersionConflict
	}
	r, err := checkinVerified(row)
	if err != nil {
		return nil, "", err
	}
	current, e := q.GetSiteCheckinConfig(ctx, id)
	if e != nil {
		return nil, "", ports.ErrUnavailable
	}
	if _, e = checkinSnapshot(ctx, q, current); e != nil {
		return nil, "", e
	}
	if !current.CreatedAt.Valid || !current.UpdatedAt.Valid || r.Config.Version > current.Version || !r.Config.CreatedAt.Equal(current.CreatedAt.Time) || r.Config.UpdatedAt.After(current.UpdatedAt.Time) {
		return nil, "", ports.ErrEndpointVersionConflict
	}
	return r, row.RequestHash, nil
}
func (s *SiteCheckinConfigStore) ReadSiteCheckinConfigReceipt(ctx context.Context, actor *ports.Actor, id uuid.UUID, key string) (out *ports.SiteCheckinConfigReceipt, err error) {
	defer func() { err = statsError(err) }()
	a, err := checkinActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil || !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	out, _, err = checkinReceipt(ctx, q, uuid.MustParse(a.ID), id, key)
	if err != nil {
		return nil, err
	}
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
func (s *SiteCheckinConfigStore) ReadSiteCheckinConfig(ctx context.Context, actor *ports.Actor, id uuid.UUID) (out *ports.SiteCheckinConfig, err error) {
	defer func() { err = statsError(err) }()
	a, err := checkinActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	if _, err = q.LockSearchIndexer(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	row, err := q.GetSiteCheckinConfig(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = statsGuard(ctx, q, a); err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	out, err = checkinSnapshot(ctx, q, row)
	if err != nil {
		return nil, err
	}
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
func (s *SiteCheckinConfigStore) SaveSiteCheckinConfig(ctx context.Context, actor *ports.Actor, id uuid.UUID, in ports.SiteCheckinConfigInput, key string) (out *ports.SiteCheckinConfigReceipt, err error) {
	defer func() { err = statsError(err) }()
	a, err := checkinActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil || !checkinValid(in) || !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.ids == nil {
		return nil, ports.ErrUnavailable
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	// The source aggregate serializes absent-row creation across administrators.
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "site_checkin_config", AggregateID: id.String()}); err != nil {
		return nil, err
	}
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	old, hash, err := checkinReceipt(ctx, q, uuid.MustParse(a.ID), id, key)
	if err == nil {
		if hash != checkinHash(in) {
			return nil, ports.ErrEndpointVersionConflict
		}
		return old, tx.Commit(ctx)
	}
	if !errors.Is(err, ports.ErrNotFound) {
		return nil, err
	}
	source, err := q.LockSearchIndexer(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	current, err := q.GetSiteCheckinConfig(ctx, id)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	if source.ConfigVersion != in.ExpectedSourceVersion || exists && current.Version != in.ExpectedVersion || !exists && in.ExpectedVersion != 0 {
		return nil, ports.ErrEndpointVersionConflict
	}
	if exists {
		if _, err = checkinSnapshot(ctx, q, current); err != nil {
			return nil, err
		}
	}
	known, err := q.SiteCheckinTimezoneKnown(ctx, in.Timezone)
	if err != nil {
		return nil, err
	}
	if !known {
		return nil, ports.ErrRejected
	}
	stored, err := q.GetIndexerConfiguration(ctx, id)
	if err != nil {
		return nil, err
	}
	if stored.ConfigurationVersion < 1 || stored.ConfigurationVersion > source.ConfigVersion {
		return nil, ports.ErrEndpointVersionConflict
	}
	if stored.Protocol != string(ports.IndexerProtocolNexusPHP) {
		return nil, ports.ErrRejected
	}
	if in.Enabled {
		conflict, e := q.SiteCheckinConflictingCalendar(ctx, db.SiteCheckinConflictingCalendarParams{IndexerID: id, ExpectedAccountID: in.ExpectedAccountID, Timezone: in.Timezone})
		if e != nil {
			return nil, e
		}
		if conflict {
			return nil, ports.ErrEndpointVersionConflict
		}
		if s.decoder == nil {
			return nil, ports.ErrUnavailable
		}
		resolved, modes, e := ResolveExecutableIndexerConfiguration(ctx, q, s.decoder, source)
		if e != nil {
			return nil, e
		}
		if modes.Protocol != ports.IndexerProtocolNexusPHP || !statsURL(resolved.Url) {
			return nil, ports.ErrRejected
		}
		cookie, e := s.decoder.DecryptSecret(source.ApiKeyEncrypted)
		if e != nil {
			return nil, ports.ErrUnavailable
		}
		if strings.TrimSpace(string(cookie)) == "" {
			return nil, ports.ErrRejected
		}
	}
	var saved db.SiteCheckinConfig
	if exists {
		saved, err = q.UpdateSiteCheckinConfig(ctx, db.UpdateSiteCheckinConfigParams{IndexerID: id, ExpectedAccountID: in.ExpectedAccountID, Profile: in.Profile, Enabled: in.Enabled, Timezone: in.Timezone, DailyAt: in.DailyAt, SourceConfigVersion: source.ConfigVersion})
	} else {
		saved, err = q.CreateSiteCheckinConfig(ctx, db.CreateSiteCheckinConfigParams{IndexerID: id, ExpectedAccountID: in.ExpectedAccountID, Profile: in.Profile, Enabled: in.Enabled, Timezone: in.Timezone, DailyAt: in.DailyAt, SourceConfigVersion: source.ConfigVersion})
	}
	if err != nil {
		return nil, err
	}
	c := checkinConfig(saved)
	if c.Version != in.ExpectedVersion+1 || c.SourceConfigVersion != in.ExpectedSourceVersion || c.ExpectedAccountID != in.ExpectedAccountID || c.Profile != in.Profile || c.Enabled != in.Enabled || c.Timezone != in.Timezone || c.DailyAt != in.DailyAt {
		return nil, ports.ErrEndpointVersionConflict
	}
	raw, _ := json.Marshal(c)
	input, _ := json.Marshal(in)
	if err = q.CreateSiteCheckinConfigReceipt(ctx, db.CreateSiteCheckinConfigReceiptParams{OwnerID: uuid.MustParse(a.ID), IndexerID: id, CommandKey: key, Version: c.Version, RequestHash: checkinHash(in), Input: input, Snapshot: raw}); err != nil {
		return nil, err
	}
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "site.checkin.config.saved", TargetType: "indexer", TargetID: id.String(), Payload: raw}); err != nil {
		return nil, err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "site_checkin_config", AggregateID: id.String()})
	if err != nil {
		return nil, err
	}
	if _, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "site_checkin_config", AggregateID: id.String(), EventType: "site.checkin.config.saved", Seq: int64(seq), Payload: raw}); err != nil {
		return nil, err
	}
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	again, err := q.LockSearchIndexer(ctx, id)
	if err != nil {
		return nil, err
	}
	config, err := q.GetIndexerConfiguration(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Enabled {
		available, e := IndexerAvailableForExecution(ctx, q, id)
		if e != nil {
			return nil, e
		}
		if !available {
			return nil, ports.ErrEndpointVersionConflict
		}
	}
	if !reflect.DeepEqual(source, again) || !reflect.DeepEqual(stored, config) {
		return nil, ports.ErrEndpointVersionConflict
	}
	final, err := q.GetSiteCheckinConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	verified, err := checkinSnapshot(ctx, q, final)
	if err != nil {
		return nil, err
	}
	if !checkinSame(c, *verified) {
		return nil, ports.ErrEndpointVersionConflict
	}
	original, originalHash, e := checkinReceipt(ctx, q, uuid.MustParse(a.ID), id, key)
	if e != nil {
		return nil, e
	}
	if originalHash != checkinHash(in) || !checkinSame(original.Config, c) {
		return nil, ports.ErrEndpointVersionConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ports.SiteCheckinConfigReceipt{CommandKey: key, Config: c}, nil
}

func checkinDecode(raw []byte, target any) error {
	if err := statsDecode(raw, target); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	var expected map[string]json.RawMessage
	if err = json.Unmarshal(canonical, &expected); err != nil {
		return err
	}
	if len(fields) != len(expected) {
		return ports.ErrEndpointVersionConflict
	}
	for key := range expected {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			return ports.ErrEndpointVersionConflict
		}
	}
	return nil
}
