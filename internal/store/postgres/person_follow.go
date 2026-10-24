package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const personFollowNamespace = "person.follow.config:"

type PersonFollowStore struct {
	pool   *pgxpool.Pool
	ids    ports.IDGen
	outbox notificationTestPublisher
}

var _ ports.PersonFollowRepository = (*PersonFollowStore)(nil)

func NewPersonFollowStore(pool *pgxpool.Pool, ids ports.IDGen, ob notificationTestPublisher) *PersonFollowStore {
	return &PersonFollowStore{pool, ids, ob}
}

func personFollowError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	for _, known := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrRejected, ports.ErrPersonFollowConflict, ports.ErrUnavailable} {
		if errors.Is(err, known) {
			return known
		}
	}
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return ports.ErrPersonFollowConflict
	}
	return ports.ErrUnavailable
}
func personFollowActor(a *ports.Actor) (ports.Actor, error) {
	if err := acquisitionCommandActor(a); err != nil {
		return ports.Actor{}, err
	}
	copy := *a
	if uuid.MustParse(copy.ID).String() != copy.ID {
		return ports.Actor{}, ports.ErrForbidden
	}
	return copy, nil
}
func personFollowKey(key string) bool {
	if len(key) == 0 || len(key) > 160 {
		return false
	}
	for _, b := range []byte(key) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}
func personFollowInput(in ports.PersonFollowInput) bool {
	return in.Settings.Validate() == nil && in.ExpectedVersion >= 0 && in.ExpectedVersion < math.MaxInt64
}
func personFollowHash(id uuid.UUID, in ports.PersonFollowInput) string {
	raw, _ := json.Marshal([]any{"personfollow.config.v1", id, in})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func personFollowLockOwner(ctx context.Context, q *db.Queries, a *ports.Actor) error {
	row, err := q.LockPersonFollowOwner(ctx, uuid.MustParse(a.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrForbidden
	}
	if err != nil {
		return err
	}
	if !row.IsActive || row.Role != a.Role {
		return ports.ErrForbidden
	}
	return nil
}
func (s *PersonFollowStore) begin(ctx context.Context, a *ports.Actor) (pgx.Tx, *db.Queries, error) {
	if s == nil || s.pool == nil {
		return nil, nil, ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	q := db.New(tx)
	if err = personFollowLockOwner(ctx, q, a); err != nil {
		managementRollback(ctx, tx)
		return nil, nil, err
	}
	isolated, err := q.GuardDoubanSyncIsolation(ctx)
	if err != nil || isolated {
		managementRollback(ctx, tx)
		if err == nil {
			err = ports.ErrUnavailable
		}
		return nil, nil, err
	}
	return tx, q, nil
}
func personFollowID(v pgtype.UUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := uuid.UUID(v.Bytes)
	return &id
}
func personFollowSnapshot(ctx context.Context, q *db.Queries, r db.PersonFollowConfig) (ports.PersonFollowConfig, error) {
	c := ports.PersonFollowConfig{ID: r.ID, OwnerID: r.OwnerID, Version: r.Version, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time, Settings: domain.Settings{PersonID: r.PersonID, Role: r.Role, Enabled: r.Enabled, MinReleaseDate: r.MinReleaseDate.Time.Format("2006-01-02"), IntervalMinutes: int(r.IntervalMinutes), MovieRootID: personFollowID(r.MovieRootID), TVRootID: personFollowID(r.TvRootID), QualityPolicyID: r.QualityPolicyID, QualityPolicyRevision: r.QualityPolicyRevision}}
	ids, err := q.ListPersonFollowIndexers(ctx, r.ID)
	if err != nil {
		return c, err
	}
	if r.BindingsConfigured {
		if ids == nil {
			ids = []uuid.UUID{}
		}
		c.Settings.Bindings = &domain.Bindings{IndexerIDs: ids, DownloaderID: personFollowID(r.DownloaderID)}
	} else if len(ids) != 0 || r.DownloaderID.Valid {
		return c, ports.ErrUnavailable
	}
	if !r.MinReleaseDate.Valid || !r.CreatedAt.Valid || !r.UpdatedAt.Valid || !personFollowValidConfig(c, r.OwnerID) {
		return c, ports.ErrUnavailable
	}
	return c, nil
}
func personFollowValidConfig(c ports.PersonFollowConfig, owner uuid.UUID) bool {
	return c.ID != uuid.Nil && c.OwnerID == owner && c.Version > 0 && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt) && c.Settings.Validate() == nil
}
func personFollowSameConfig(a, b ports.PersonFollowConfig) bool {
	return a.ID == b.ID && a.OwnerID == b.OwnerID && a.Version == b.Version && a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt) && reflect.DeepEqual(a.Settings, b.Settings)
}

func personFollowReceipt(ctx context.Context, q *db.Queries, a *ports.Actor, key string) (*ports.PersonFollowReceipt, string, error) {
	owner := uuid.MustParse(a.ID)
	command := personFollowNamespace + key
	history, hErr := q.ReadPersonFollowHistory(ctx, db.ReadPersonFollowHistoryParams{OwnerID: owner, CommandKey: command})
	old, rErr := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: command})
	if errors.Is(hErr, pgx.ErrNoRows) && errors.Is(rErr, pgx.ErrNoRows) {
		return nil, "", ports.ErrNotFound
	}
	if hErr != nil && !errors.Is(hErr, pgx.ErrNoRows) {
		return nil, "", hErr
	}
	if rErr != nil && !errors.Is(rErr, pgx.ErrNoRows) {
		return nil, "", rErr
	}
	if hErr != nil || rErr != nil {
		return nil, "", ports.ErrPersonFollowConflict
	}
	var receipt struct {
		Contract string                    `json:"contract"`
		Receipt  ports.PersonFollowReceipt `json:"receipt"`
	}
	var snapshot ports.PersonFollowConfig
	var input ports.PersonFollowInput
	if json.Unmarshal(old.ResponseBody, &receipt) != nil || receipt.Contract != "personfollow.config.v1" || receipt.Receipt.CommandKey != key || old.ResponseCode != 200 || !old.ResourceID.Valid || old.ResourceID.String != history.ConfigID.String() || old.RequestHash != history.RequestHash || json.Unmarshal(history.Input, &input) != nil || !personFollowInput(input) || json.Unmarshal(history.Snapshot, &snapshot) != nil || snapshot.ID != history.ConfigID || snapshot.Version != history.Version || snapshot.Version != input.ExpectedVersion+1 || !personFollowValidConfig(snapshot, owner) || !personFollowSameConfig(snapshot, receipt.Receipt.Config) || personFollowHash(history.ConfigID, input) != history.RequestHash {
		return nil, "", ports.ErrPersonFollowConflict
	}
	expected := input.Settings
	if expected.Bindings == nil {
		expected.Bindings = snapshot.Settings.Bindings
	}
	if !reflect.DeepEqual(expected, snapshot.Settings) {
		return nil, "", ports.ErrPersonFollowConflict
	}
	return &receipt.Receipt, history.RequestHash, nil
}

func personFollowTargets(ctx context.Context, q *db.Queries, s domain.Settings) error {
	policy, err := q.LockSearchPolicy(ctx, s.QualityPolicyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	if err != nil {
		return err
	}
	if s.Enabled && policy.CurrentRevision != s.QualityPolicyRevision {
		return ports.ErrPersonFollowConflict
	}
	if _, err = q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: s.QualityPolicyID, Revision: s.QualityPolicyRevision}); errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	} else if err != nil {
		return err
	}
	roots := []uuid.UUID{}
	if s.MovieRootID != nil {
		roots = append(roots, *s.MovieRootID)
	}
	if s.TVRootID != nil {
		roots = append(roots, *s.TVRootID)
	}
	locked, err := q.LockDoubanSyncRoots(ctx, roots)
	if err != nil {
		return err
	}
	for _, target := range []struct {
		id   *uuid.UUID
		kind string
	}{{s.MovieRootID, "movie"}, {s.TVRootID, "tv"}} {
		if target.id == nil {
			continue
		}
		found := false
		for _, root := range locked {
			if root.ID == *target.id {
				found = true
				if root.MediaType != target.kind || s.Enabled && root.Health != "online" {
					return ports.ErrRejected
				}
			}
		}
		if !found {
			return ports.ErrNotFound
		}
	}
	if s.Bindings != nil {
		return doubanSyncLockBindings(ctx, q, &ports.DoubanSyncBindings{IndexerIDs: s.Bindings.IndexerIDs, DownloaderID: s.Bindings.DownloaderID}, s.Enabled)
	}
	return nil
}

func (s *PersonFollowStore) SavePersonFollow(ctx context.Context, actor *ports.Actor, id uuid.UUID, input ports.PersonFollowInput, key string) (result *ports.PersonFollowReceipt, err error) {
	defer func() { err = personFollowError(ctx, err) }()
	a, e := personFollowActor(actor)
	if e != nil {
		return nil, e
	}
	in := input
	in.Settings = in.Settings.Normalized()
	if id == uuid.Nil || !personFollowInput(in) || !personFollowKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.ids == nil || s.outbox == nil {
		return nil, ports.ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, q, e := s.begin(bounded, &a)
	if e != nil {
		return nil, e
	}
	defer managementRollback(bounded, tx)
	old, hash, e := personFollowReceipt(bounded, q, &a, key)
	if e == nil {
		if hash != personFollowHash(id, in) {
			return nil, ports.ErrPersonFollowConflict
		}
		return old, nil
	}
	if !errors.Is(e, ports.ErrNotFound) {
		return nil, e
	}
	current, e := q.LockPersonFollowConfig(bounded, id)
	exists := e == nil
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	owner := uuid.MustParse(a.ID)
	if exists && current.OwnerID != owner {
		return nil, ports.ErrNotFound
	}
	if exists && current.Version != in.ExpectedVersion || !exists && in.ExpectedVersion != 0 {
		return nil, ports.ErrPersonFollowConflict
	}
	if exists && (current.PersonID != in.Settings.PersonID || current.Role != in.Settings.Role) {
		return nil, ports.ErrPersonFollowConflict
	}
	effective := in.Settings.Clone()
	if exists && effective.Bindings == nil {
		prior, e := personFollowSnapshot(bounded, q, current)
		if e != nil {
			return nil, e
		}
		effective.Bindings = prior.Settings.Bindings
	}
	if !exists {
		count, e := q.CountPersonFollowConfigs(bounded, owner)
		if e != nil {
			return nil, e
		}
		if count >= 100 {
			return nil, ports.ErrPersonFollowConflict
		}
	}
	if e = personFollowTargets(bounded, q, effective); e != nil {
		return nil, e
	}
	var downloader *uuid.UUID
	if effective.Bindings != nil {
		downloader = effective.Bindings.DownloaderID
	}
	date, _ := time.Parse("2006-01-02", effective.MinReleaseDate)
	row, e := q.UpsertPersonFollowConfig(bounded, db.UpsertPersonFollowConfigParams{ID: id, OwnerID: owner, PersonID: effective.PersonID, Role: effective.Role, Enabled: effective.Enabled, MinReleaseDate: pgtype.Date{Time: date, Valid: true}, IntervalMinutes: int32(effective.IntervalMinutes), MovieRootID: doubanSyncNullable(effective.MovieRootID), TvRootID: doubanSyncNullable(effective.TVRootID), QualityPolicyID: effective.QualityPolicyID, QualityPolicyRevision: effective.QualityPolicyRevision, BindingsConfigured: effective.Bindings != nil, DownloaderID: doubanSyncNullable(downloader), ExpectedVersion: in.ExpectedVersion})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrPersonFollowConflict
	}
	if e != nil {
		return nil, e
	}
	if e = q.ClearPersonFollowIndexers(bounded, id); e != nil {
		return nil, e
	}
	if effective.Bindings != nil {
		for _, source := range effective.Bindings.IndexerIDs {
			if e = q.AddPersonFollowIndexer(bounded, db.AddPersonFollowIndexerParams{ConfigID: id, IndexerID: source}); e != nil {
				return nil, e
			}
		}
	}
	config, e := personFollowSnapshot(bounded, q, row)
	if e != nil {
		return nil, e
	}
	result = &ports.PersonFollowReceipt{CommandKey: key, Config: config}
	inputRaw, _ := json.Marshal(in)
	snapshot, _ := json.Marshal(config)
	requestHash := personFollowHash(id, in)
	command := personFollowNamespace + key
	if e = q.CreatePersonFollowHistory(bounded, db.CreatePersonFollowHistoryParams{ConfigID: id, OwnerID: owner, Version: config.Version, CommandKey: command, RequestHash: requestHash, Input: inputRaw, Snapshot: snapshot}); e != nil {
		return nil, e
	}
	if _, e = s.outbox.Publish(bounded, q, "personfollow.config", id.String(), "personfollow.config.saved", map[string]any{"version": config.Version, "enabled": effective.Enabled}, nil); e != nil {
		return nil, e
	}
	if _, e = q.InsertAuditLog(bounded, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "personfollow.config.saved", TargetType: "person_follow_config", TargetID: id.String(), Payload: []byte("{}")}); e != nil {
		return nil, e
	}
	raw, _ := json.Marshal(struct {
		Contract string                    `json:"contract"`
		Receipt  ports.PersonFollowReceipt `json:"receipt"`
	}{"personfollow.config.v1", *result})
	if _, e = q.CreateIdempotencyRecord(bounded, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: command, RequestHash: requestHash, ResponseCode: 200, ResponseBody: raw, ResourceID: pgtype.Text{String: id.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = personFollowLockOwner(bounded, q, &a); e != nil {
		return nil, e
	}
	if e = personFollowTargets(bounded, q, effective); e != nil {
		return nil, e
	}
	if e = tx.Commit(bounded); e != nil {
		return nil, e
	}
	return result, nil
}

func (s *PersonFollowStore) ReadPersonFollow(ctx context.Context, actor *ports.Actor, id uuid.UUID) (result *ports.PersonFollowConfig, err error) {
	defer func() { err = personFollowError(ctx, err) }()
	a, e := personFollowActor(actor)
	if e != nil {
		return nil, e
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	tx, q, e := s.begin(ctx, &a)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	row, e := q.ReadPersonFollowConfig(ctx, db.ReadPersonFollowConfigParams{ID: id, OwnerID: uuid.MustParse(a.ID)})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	config, e := personFollowSnapshot(ctx, q, row)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &config, nil
}
func (s *PersonFollowStore) ListPersonFollows(ctx context.Context, actor *ports.Actor) (result []ports.PersonFollowConfig, err error) {
	defer func() { err = personFollowError(ctx, err) }()
	a, e := personFollowActor(actor)
	if e != nil {
		return nil, e
	}
	tx, q, e := s.begin(ctx, &a)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	rows, e := q.ListPersonFollowConfigs(ctx, uuid.MustParse(a.ID))
	if e != nil {
		return nil, e
	}
	if len(rows) > 100 {
		return nil, ports.ErrUnavailable
	}
	result = make([]ports.PersonFollowConfig, 0, len(rows))
	for _, row := range rows {
		config, e := personFollowSnapshot(ctx, q, row)
		if e != nil {
			return nil, e
		}
		result = append(result, config)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return result, nil
}
func (s *PersonFollowStore) ReadPersonFollowReceipt(ctx context.Context, actor *ports.Actor, key string) (result *ports.PersonFollowReceipt, err error) {
	defer func() { err = personFollowError(ctx, err) }()
	a, e := personFollowActor(actor)
	if e != nil {
		return nil, e
	}
	if !personFollowKey(key) {
		return nil, ports.ErrRejected
	}
	tx, q, e := s.begin(ctx, &a)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	result, _, e = personFollowReceipt(ctx, q, &a, key)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return result, nil
}
