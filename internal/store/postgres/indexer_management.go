package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IndexerManagementStore owns the account, command, endpoint and event transaction.
type IndexerManagementStore struct {
	pool    *pgxpool.Pool
	ids     ports.IDGen
	decoder ports.CredentialDecoder
}

func NewIndexerManagementStore(pool *pgxpool.Pool, ids ports.IDGen, decoder ports.CredentialDecoder) *IndexerManagementStore {
	return &IndexerManagementStore{pool: pool, ids: ids, decoder: decoder}
}

var _ ports.IndexerManagementRepository = (*IndexerManagementStore)(nil)
var _ ports.IndexerManagementReceiptReader = (*IndexerManagementStore)(nil)

// IndexerAvailableForExecution retains the caller's transaction and snapshot.
// Historical fixtures and legacy rows without lifecycle metadata remain available.
func IndexerAvailableForExecution(ctx context.Context, q *db.Queries, id uuid.UUID) (bool, error) {
	if q == nil {
		return false, ports.ErrUnavailable
	}
	v, err := q.IndexerLifecycleSchemaAvailable(ctx)
	if err != nil {
		return false, ports.ErrUnavailable
	}
	present, ok := v.(bool)
	if !ok {
		return false, ports.ErrUnavailable
	}
	if !present {
		return true, nil
	}
	available, err := q.IndexerLifecycleAvailable(ctx, id)
	if err != nil {
		return false, ports.ErrUnavailable
	}
	return available, nil
}

func managementActor(a *ports.Actor) error {
	if err := acquisitionCommandActor(a); err != nil {
		return err
	}
	if a.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return nil
}
func managementError(err error) error {
	if err == nil || errors.Is(err, ports.ErrUnauthorized) || errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrRejected) || errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrIndexerCommandConflict) {
		return err
	}
	return ports.ErrUnavailable
}
func managementRollback(ctx context.Context, tx pgx.Tx) {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(c)
}
func managementName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) > 0 && utf8.RuneCountInString(name) <= 256 && strings.TrimSpace(name) == name && !strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) })
}
func managementKey(key, hash string) bool {
	if len(key) < 8 || len(key) > 130 || len(hash) != 64 {
		return false
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return false
	}
	for _, r := range key {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:", r)
		if !allowed {
			return false
		}
	}
	return true
}
func managementResponseCode(action ports.IndexerMutationAction) int32 {
	if action == ports.IndexerCreate {
		return 201
	}
	return 200
}

func (s *IndexerManagementStore) MutateIndexer(ctx context.Context, a *ports.Actor, m ports.IndexerMutation, key, hash string) (out *ports.IndexerManagementReceipt, err error) {
	defer func() { err = managementError(err) }()
	if err = managementActor(a); err != nil {
		return nil, err
	}
	if !managementKey(key, hash) || (m.Action != ports.IndexerCreate && m.Action != ports.IndexerUpdate && m.Action != ports.IndexerDelete) {
		return nil, ports.ErrRejected
	}
	if m.Action == ports.IndexerCreate {
		if m.IndexerID != uuid.Nil || m.ExpectedVersion != 0 || m.PreserveURL || m.PreserveCredential {
			return nil, ports.ErrRejected
		}
	} else if m.IndexerID == uuid.Nil || m.ExpectedVersion < 1 {
		return nil, ports.ErrRejected
	}
	if s == nil || s.pool == nil || s.ids == nil || s.decoder == nil {
		return nil, ports.ErrUnavailable
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	// The account lock is acquired before command or endpoint locks. This function
	// checks the CURRENT database role after any wait, including receipt recovery.
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	material, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "indexer.management.command", AggregateID: string(material)}); e != nil {
		return nil, e
	}
	commandKey := "indexer." + string(m.Action) + ":" + key
	actionLock, _ := json.Marshal([]string{string(a.Type), a.ID, commandKey})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "indexer.management.command", AggregateID: string(actionLock)}); e != nil {
		return nil, e
	}
	// A raw key also binds its action: changing action cannot bypass the hash fence.
	for _, action := range []ports.IndexerMutationAction{ports.IndexerCreate, ports.IndexerUpdate, ports.IndexerDelete} {
		old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "indexer." + string(action) + ":" + key})
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if action != m.Action || old.RequestHash != hash || old.ResponseCode != managementResponseCode(m.Action) {
			return nil, ports.ErrIndexerCommandConflict
		}
		r, e := managementDecodeReceipt(old.ResponseBody)
		if e != nil || r.Action != m.Action || !old.ResourceID.Valid || old.ResourceID.String != r.Indexer.ID.String() || (m.Action != ports.IndexerCreate && r.Indexer.ID != m.IndexerID) {
			return nil, ports.ErrIndexerCommandConflict
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return r, nil
	}
	var row db.Indexer
	var config db.IndexerConfiguration
	if m.Action != ports.IndexerCreate {
		row, e = q.LockIndexerForEdit(ctx, m.IndexerID)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		if e != nil {
			return nil, e
		}
		available, e := IndexerAvailableForExecution(ctx, q, row.ID)
		if e != nil {
			return nil, e
		}
		if !available {
			return nil, ports.ErrNotFound
		}
		if row.ConfigVersion != m.ExpectedVersion {
			return nil, ports.ErrIndexerCommandConflict
		}
		config, e = q.GetIndexerConfiguration(ctx, row.ID)
		if e != nil {
			return nil, e
		}
	}
	if m.Action == ports.IndexerDelete {
		row, e = q.EditIndexer(ctx, db.EditIndexerParams{ID: row.ID, Name: row.Name, Url: row.Url})
		if e != nil {
			return nil, e
		}
		// Advance the extension fence without changing historical endpoint material.
		n, e := q.UpdateManagedIndexerConfiguration(ctx, db.UpdateManagedIndexerConfigurationParams{IndexerID: row.ID, Protocol: config.Protocol, UrlEncrypted: config.UrlEncrypted, ConfigurationVersion: row.ConfigVersion})
		if e != nil {
			return nil, e
		}
		if n != 1 {
			return nil, ports.ErrIndexerCommandConflict
		}
		if e = q.EnsureIndexerLifecycle(ctx, row.ID); e != nil {
			return nil, e
		}
		n, e = q.TombstoneManagedIndexer(ctx, row.ID)
		if e != nil {
			return nil, e
		}
		if n != 1 {
			return nil, ports.ErrIndexerCommandConflict
		}
		if e = q.MarkManagedIndexerOffline(ctx, row.ID); e != nil {
			return nil, e
		}
		row.Health = "offline"
		row.RssEnabled = false
	} else {
		c := m.Configuration
		if !managementName(m.Name) || !validIndexerProtocol(c.Protocol) || c.Categories == nil {
			return nil, ports.ErrRejected
		}
		for _, category := range c.Categories {
			if category < 1 || int64(category) > 2147483647 {
				return nil, ports.ErrRejected
			}
		}
		if m.PreserveURL {
			if len(c.URLCiphertext) != 0 || c.DisplayURL != "" {
				return nil, ports.ErrRejected
			}
			c.URLCiphertext = config.UrlEncrypted
			c.DisplayURL = row.Url
		}
		if m.PreserveCredential {
			if len(c.CredentialCiphertext) != 0 {
				return nil, ports.ErrRejected
			}
			c.CredentialCiphertext = row.ApiKeyEncrypted
		}
		origin, e := indexerConfigurationOrigin(c.DisplayURL)
		if e != nil || origin != c.DisplayURL || len(c.URLCiphertext) == 0 || len(c.CredentialCiphertext) == 0 {
			return nil, ports.ErrRejected
		}
		plain, e := s.decoder.DecryptSecret(c.URLCiphertext)
		if e != nil {
			return nil, ports.ErrUnavailable
		}
		origin, e = indexerConfigurationOrigin(string(plain))
		if e != nil || origin != c.DisplayURL {
			return nil, ports.ErrRejected
		}
		if _, e = s.decoder.DecryptSecret(c.CredentialCiphertext); e != nil {
			return nil, ports.ErrUnavailable
		}
		categories, _ := json.Marshal(c.Categories)
		if m.Action == ports.IndexerCreate {
			row, e = q.CreateIndexer(ctx, db.CreateIndexerParams{ID: s.ids.NewUUID(), Name: m.Name, Url: c.DisplayURL, ApiKeyEncrypted: c.CredentialCiphertext, Categories: categories})
			if e != nil {
				return nil, e
			}
			if e = q.InsertIndexerConfiguration(ctx, db.InsertIndexerConfigurationParams{IndexerID: row.ID, Protocol: string(c.Protocol), UrlEncrypted: c.URLCiphertext, BehaviorVersion: "explicit-v1", ConfigurationVersion: row.ConfigVersion}); e != nil {
				return nil, e
			}
			if e = q.CreateIndexerLifecycle(ctx, row.ID); e != nil {
				return nil, e
			}
		} else {
			row, e = q.EditIndexer(ctx, db.EditIndexerParams{ID: row.ID, Name: m.Name, Url: c.DisplayURL, ApiKeyEncrypted: c.CredentialCiphertext, Categories: categories})
			if e != nil {
				return nil, e
			}
			n, e := q.UpdateManagedIndexerConfiguration(ctx, db.UpdateManagedIndexerConfigurationParams{IndexerID: row.ID, Protocol: string(c.Protocol), UrlEncrypted: c.URLCiphertext, ConfigurationVersion: row.ConfigVersion})
			if e != nil {
				return nil, e
			}
			if n != 1 {
				return nil, ports.ErrIndexerCommandConflict
			}
		}
	}
	view, e := s.managementView(ctx, q, row)
	if e != nil {
		return nil, e
	}
	out = &ports.IndexerManagementReceipt{Action: m.Action, Indexer: view, Deleted: m.Action == ports.IndexerDelete}
	event := "indexer.configuration_" + map[ports.IndexerMutationAction]string{ports.IndexerCreate: "created", ports.IndexerUpdate: "updated", ports.IndexerDelete: "deleted"}[m.Action]
	safe, _ := json.Marshal(struct {
		ID       uuid.UUID             `json:"indexer_id"`
		Version  int64                 `json:"config_version"`
		Protocol ports.IndexerProtocol `json:"protocol"`
	}{row.ID, row.ConfigVersion, view.Protocol})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: event, TargetType: "indexer", TargetID: row.ID.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "indexer", AggregateID: row.ID.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "indexer", AggregateID: row.ID.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "indexer", AggregateID: row.ID.String(), Seq: int64(seq), EventType: event, Payload: safe}); e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(out)
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: commandKey, RequestHash: hash, ResponseCode: managementResponseCode(m.Action), ResponseBody: raw, ResourceID: pgtype.Text{String: row.ID.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

func (s *IndexerManagementStore) managementView(ctx context.Context, q *db.Queries, row db.Indexer) (ports.IndexerManagementView, error) {
	var v ports.IndexerManagementView
	_, modes, err := ResolveIndexerConfiguration(ctx, q, s.decoder, row)
	if err != nil {
		return v, ports.ErrUnavailable
	}
	origin, err := indexerConfigurationOrigin(row.Url)
	if err != nil || origin != row.Url {
		return v, ports.ErrUnavailable
	}
	categories := []int{}
	if err := json.Unmarshal(row.Categories, &categories); err != nil {
		return v, ports.ErrUnavailable
	}
	if categories == nil {
		categories = []int{}
	}
	v = ports.IndexerManagementView{ID: row.ID, Name: row.Name, URL: origin, Protocol: modes.Protocol, Categories: categories, ConfigVersion: row.ConfigVersion, Health: row.Health, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, LegacyV1: modes.LegacyV1, HealthFailures: row.HealthFailures, HealthReason: row.HealthReason}
	if row.HealthCheckedAt.Valid {
		v.HealthCheckedAt = &row.HealthCheckedAt.Time
	}
	if row.HealthObservedAt.Valid {
		v.HealthObservedAt = &row.HealthObservedAt.Time
	}
	if row.HealthNextCheckAt.Valid {
		v.HealthNextCheckAt = &row.HealthNextCheckAt.Time
	}
	if !managementSafeView(v) {
		return ports.IndexerManagementView{}, ports.ErrUnavailable
	}
	return v, nil
}
func managementSafeView(v ports.IndexerManagementView) bool {
	origin, e := indexerConfigurationOrigin(v.URL)
	if e != nil || origin != v.URL || v.ID == uuid.Nil || v.ConfigVersion < 1 || !validIndexerProtocol(v.Protocol) || !managementName(v.Name) || v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() {
		return false
	}
	if v.Categories == nil {
		return false
	}
	for _, category := range v.Categories {
		if category < 1 || int64(category) > 2147483647 {
			return false
		}
	}
	switch v.Health {
	case "online", "offline", "rate_limited", "stale":
	default:
		return false
	}
	switch v.HealthReason {
	case "not_checked", "ok", "unavailable", "rate_limited":
	default:
		return false
	}
	return true
}
func managementDecodeReceipt(raw []byte) (*ports.IndexerManagementReceipt, error) {
	var r ports.IndexerManagementReceipt
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil || dec.Decode(new(any)) != io.EOF || !managementSafeView(r.Indexer) || (r.Action != ports.IndexerCreate && r.Action != ports.IndexerUpdate && r.Action != ports.IndexerDelete) || r.Deleted != (r.Action == ports.IndexerDelete) {
		return nil, ports.ErrIndexerCommandConflict
	}
	return &r, nil
}

func (s *IndexerManagementStore) ListManagedIndexers(ctx context.Context, a *ports.Actor) (out []ports.IndexerManagementView, err error) {
	defer func() { err = managementError(err) }()
	if err = managementActor(a); err != nil {
		return nil, err
	}
	if s == nil || s.pool == nil || s.decoder == nil {
		return nil, ports.ErrUnavailable
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	ids, e := q.ListManagedIndexerIDs(ctx)
	if e != nil {
		return nil, e
	}
	out = []ports.IndexerManagementView{}
	for _, id := range ids {
		row, e := q.GetIndexerByID(ctx, id)
		if e != nil {
			return nil, e
		}
		view, e := s.managementView(ctx, q, row)
		if e != nil {
			return nil, e
		}
		out = append(out, view)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}

// ReadIndexerManagementReceipt recovers a committed safe receipt under the
// current administrator account lock, independently of endpoint state/version.
func (s *IndexerManagementStore) ReadIndexerManagementReceipt(ctx context.Context, a *ports.Actor, action ports.IndexerMutationAction, key string) (out *ports.IndexerManagementReceipt, err error) {
	defer func() { err = managementError(err) }()
	if err = managementActor(a); err != nil {
		return nil, err
	}
	if !managementKey(key, strings.Repeat("0", 64)) || (action != ports.IndexerCreate && action != ports.IndexerUpdate && action != ports.IndexerDelete) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: "indexer." + string(action) + ":" + key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	out, e = managementDecodeReceipt(old.ResponseBody)
	if e != nil || out.Action != action || old.ResponseCode != managementResponseCode(action) || !old.ResourceID.Valid || old.ResourceID.String != out.Indexer.ID.String() {
		return nil, ports.ErrIndexerCommandConflict
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
