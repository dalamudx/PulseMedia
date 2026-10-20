package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/indexer"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SiteAccountStatsConfigStore struct {
	pool    *pgxpool.Pool
	ids     ports.IDGen
	decoder ports.CredentialDecoder
}

var _ ports.SiteAccountStatsConfigRepository = (*SiteAccountStatsConfigStore)(nil)

func NewSiteAccountStatsConfigStore(pool *pgxpool.Pool, ids ports.IDGen, decoder ports.CredentialDecoder) *SiteAccountStatsConfigStore {
	return &SiteAccountStatsConfigStore{pool, ids, decoder}
}
func statsActor(actor *ports.Actor) (*ports.Actor, error) {
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	a := *actor
	if err := managementActor(&a); err != nil {
		return nil, err
	}
	return &a, nil
}
func statsValid(in ports.SiteAccountStatsConfigInput) bool {
	return indexer.ValidSiteAccountID(in.ExpectedAccountID) && in.Profile == ports.NexusPHPClassicAccountStatsProfile && in.IntervalSeconds >= 900 && in.IntervalSeconds <= 86400 && in.ExpectedVersion >= 0 && in.ExpectedVersion < math.MaxInt64 && in.ExpectedSourceVersion >= 1
}
func statsHash(in ports.SiteAccountStatsConfigInput) string {
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func statsError(err error) error {
	if err == nil {
		return nil
	}
	for _, e := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrRejected, ports.ErrNotFound, ports.ErrEndpointVersionConflict} {
		if errors.Is(err, e) {
			return e
		}
	}
	return ports.ErrUnavailable
}
func statsGuard(ctx context.Context, q *db.Queries, a *ports.Actor) error {
	isolated, err := q.GuardDoubanSyncIsolation(ctx)
	if err != nil || isolated {
		return ports.ErrUnavailable
	}
	return lockAcquisitionUser(ctx, q, a)
}
func (s *SiteAccountStatsConfigStore) begin(ctx context.Context, a *ports.Actor) (pgx.Tx, *db.Queries, error) {
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
func statsConfig(row db.SiteAccountStatsConfig) ports.SiteAccountStatsConfig {
	return ports.SiteAccountStatsConfig{IndexerID: row.IndexerID, ExpectedAccountID: row.ExpectedAccountID, Profile: row.Profile, Enabled: row.Enabled, IntervalSeconds: row.IntervalSeconds, Version: row.Version, SourceConfigVersion: row.SourceConfigVersion, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
}

// Compare instants before normalizing only local copies; return the original JSON timestamps.
func statsSame(a, b ports.SiteAccountStatsConfig) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	a.CreatedAt = b.CreatedAt
	a.UpdatedAt = b.UpdatedAt
	return a == b
}
func statsVerified(row db.SiteAccountStatsConfigReceipt) (*ports.SiteAccountStatsConfigReceipt, error) {
	var in ports.SiteAccountStatsConfigInput
	var c ports.SiteAccountStatsConfig
	if statsDecode(row.Input, &in) != nil || statsDecode(row.Snapshot, &c) != nil || !statsValid(in) || statsHash(in) != row.RequestHash || row.OwnerID == uuid.Nil || row.IndexerID == uuid.Nil || !doubanSyncASCII(row.CommandKey, 160, false) || c.IndexerID != row.IndexerID || c.Version != row.Version || c.Version != in.ExpectedVersion+1 || c.SourceConfigVersion != in.ExpectedSourceVersion || c.ExpectedAccountID != in.ExpectedAccountID || c.Profile != in.Profile || c.Enabled != in.Enabled || c.IntervalSeconds != in.IntervalSeconds || c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) {
		return nil, ports.ErrEndpointVersionConflict
	}
	return &ports.SiteAccountStatsConfigReceipt{CommandKey: row.CommandKey, Config: c}, nil
}
func statsSnapshot(ctx context.Context, q *db.Queries, row db.SiteAccountStatsConfig) (*ports.SiteAccountStatsConfig, error) {
	receipt, err := q.GetSiteAccountStatsConfigReceiptVersion(ctx, db.GetSiteAccountStatsConfigReceiptVersionParams{IndexerID: row.IndexerID, Version: row.Version})
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	r, err := statsVerified(receipt)
	if err != nil {
		return nil, err
	}
	if !row.CreatedAt.Valid || !row.UpdatedAt.Valid || !statsSame(r.Config, statsConfig(row)) {
		return nil, ports.ErrEndpointVersionConflict
	}
	return &r.Config, nil
}
func statsReceipt(ctx context.Context, q *db.Queries, owner, id uuid.UUID, key string) (*ports.SiteAccountStatsConfigReceipt, string, error) {
	row, err := q.GetSiteAccountStatsConfigReceipt(ctx, db.GetSiteAccountStatsConfigReceiptParams{OwnerID: owner, IndexerID: id, CommandKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ports.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if row.OwnerID != owner || row.IndexerID != id || row.CommandKey != key {
		return nil, "", ports.ErrEndpointVersionConflict
	}
	r, err := statsVerified(row)
	if err != nil {
		return nil, "", err
	}
	current, e := q.GetSiteAccountStatsConfig(ctx, id)
	if e != nil {
		return nil, "", ports.ErrUnavailable
	}
	if !current.CreatedAt.Valid || !current.UpdatedAt.Valid || r.Config.Version > current.Version || !r.Config.CreatedAt.Equal(current.CreatedAt.Time) || r.Config.UpdatedAt.After(current.UpdatedAt.Time) {
		return nil, "", ports.ErrEndpointVersionConflict
	}
	return r, row.RequestHash, nil
}
func (s *SiteAccountStatsConfigStore) ReadSiteAccountStatsConfigReceipt(ctx context.Context, actor *ports.Actor, id uuid.UUID, key string) (out *ports.SiteAccountStatsConfigReceipt, err error) {
	defer func() { err = statsError(err) }()
	a, err := statsActor(actor)
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
	out, _, err = statsReceipt(ctx, q, uuid.MustParse(a.ID), id, key)
	if err != nil {
		return nil, err
	}
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
func (s *SiteAccountStatsConfigStore) ReadSiteAccountStatsConfig(ctx context.Context, actor *ports.Actor, id uuid.UUID) (out *ports.SiteAccountStatsConfig, err error) {
	defer func() { err = statsError(err) }()
	a, err := statsActor(actor)
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
	row, err := q.GetSiteAccountStatsConfig(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = statsGuard(ctx, q, a); err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	out, err = statsSnapshot(ctx, q, row)
	if err != nil {
		return nil, err
	}
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
func statsURL(raw string) bool {
	if !utf8.ValidString(raw) || strings.ContainsFunc(raw, unicode.IsControl) || strings.Contains(raw, "\\") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.Contains(u.Path, "%") || strings.Contains(u.Path, "//") {
		return false
	}
	for _, p := range strings.Split(u.Path, "/") {
		if p == "." || p == ".." {
			return false
		}
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}
func (s *SiteAccountStatsConfigStore) SaveSiteAccountStatsConfig(ctx context.Context, actor *ports.Actor, id uuid.UUID, in ports.SiteAccountStatsConfigInput, key string) (out *ports.SiteAccountStatsConfigReceipt, err error) {
	defer func() { err = statsError(err) }()
	a, err := statsActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil || !statsValid(in) || !doubanSyncASCII(key, 160, false) {
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
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "site_account_stats_config", AggregateID: id.String()}); err != nil {
		return nil, err
	}
	if err = statsGuard(ctx, q, a); err != nil {
		return nil, err
	}
	old, hash, err := statsReceipt(ctx, q, uuid.MustParse(a.ID), id, key)
	if err == nil {
		if hash != statsHash(in) {
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
	current, err := q.GetSiteAccountStatsConfig(ctx, id)
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
		if _, err = statsSnapshot(ctx, q, current); err != nil {
			return nil, err
		}
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
	var saved db.SiteAccountStatsConfig
	if exists {
		saved, err = q.UpdateSiteAccountStatsConfig(ctx, db.UpdateSiteAccountStatsConfigParams{IndexerID: id, ExpectedAccountID: in.ExpectedAccountID, Profile: in.Profile, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, SourceConfigVersion: source.ConfigVersion})
	} else {
		saved, err = q.CreateSiteAccountStatsConfig(ctx, db.CreateSiteAccountStatsConfigParams{IndexerID: id, ExpectedAccountID: in.ExpectedAccountID, Profile: in.Profile, Enabled: in.Enabled, IntervalSeconds: in.IntervalSeconds, SourceConfigVersion: source.ConfigVersion})
	}
	if err != nil {
		return nil, err
	}
	c := statsConfig(saved)
	if c.Version != in.ExpectedVersion+1 || c.SourceConfigVersion != in.ExpectedSourceVersion || c.ExpectedAccountID != in.ExpectedAccountID || c.Profile != in.Profile || c.Enabled != in.Enabled || c.IntervalSeconds != in.IntervalSeconds {
		return nil, ports.ErrEndpointVersionConflict
	}
	raw, _ := json.Marshal(c)
	input, _ := json.Marshal(in)
	if err = q.CreateSiteAccountStatsConfigReceipt(ctx, db.CreateSiteAccountStatsConfigReceiptParams{OwnerID: uuid.MustParse(a.ID), IndexerID: id, CommandKey: key, Version: c.Version, RequestHash: statsHash(in), Input: input, Snapshot: raw}); err != nil {
		return nil, err
	}
	if _, err = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "site.account_stats.config.saved", TargetType: "indexer", TargetID: id.String(), Payload: raw}); err != nil {
		return nil, err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "site_account_stats_config", AggregateID: id.String()})
	if err != nil {
		return nil, err
	}
	if _, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "site_account_stats_config", AggregateID: id.String(), EventType: "site.account_stats.config.saved", Seq: int64(seq), Payload: raw}); err != nil {
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
	final, err := q.GetSiteAccountStatsConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	verified, err := statsSnapshot(ctx, q, final)
	if err != nil {
		return nil, err
	}
	if !statsSame(c, *verified) {
		return nil, ports.ErrEndpointVersionConflict
	}
	original, originalHash, e := statsReceipt(ctx, q, uuid.MustParse(a.ID), id, key)
	if e != nil {
		return nil, e
	}
	if originalHash != statsHash(in) || !statsSame(original.Config, c) {
		return nil, ports.ErrEndpointVersionConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ports.SiteAccountStatsConfigReceipt{CommandKey: key, Config: c}, nil
}

func statsDecode(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(target); e != nil {
		return e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return ports.ErrEndpointVersionConflict
	}
	return nil
}
