package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	domain "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubscriptionCreationStore publishes provider observations and subscription
// intent in one transaction. Provider calls belong to its caller.
type SubscriptionCreationStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewSubscriptionCreationStore(pool *pgxpool.Pool, ids ports.IDGen) *SubscriptionCreationStore {
	return &SubscriptionCreationStore{pool: pool, ids: ids}
}

var _ ports.SubscriptionCreationRepository = (*SubscriptionCreationStore)(nil)

func creationError(ctx context.Context, err error) error {
	if errors.Is(err, ports.ErrNoSubscriptionRoot) || errors.Is(err, ports.ErrAmbiguousSubscriptionRoot) {
		return err
	}
	return subscriptionIntentError(ctx, err)
}
func creationRollback(ctx context.Context, tx pgx.Tx) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanup)
}
func creationClone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func creationKey(key string) (string, error) {
	if len(key) == 0 || len(key) > 160 || strings.TrimSpace(key) != key {
		return "", ports.ErrInvalidSubscriptionBindings
	}
	for _, c := range key {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':'
		if !allowed {
			return "", ports.ErrInvalidSubscriptionBindings
		}
	}
	return "subscription.creation:" + key, nil
}
func creationHash(hash string) bool {
	decoded, err := hex.DecodeString(hash)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(hash) == hash
}
func creationOwner(a *ports.Actor, in ports.SubscriptionCreationInput) (uuid.UUID, error) {
	if err := acquisitionCommandActor(a); err != nil {
		return uuid.Nil, err
	}
	owner := uuid.MustParse(a.ID)
	if in.UserID != nil {
		owner = *in.UserID
	}
	if owner == uuid.Nil {
		return uuid.Nil, ports.ErrInvalidSubscriptionBindings
	}
	if !a.CanAccessUser(owner.String()) {
		return uuid.Nil, ports.ErrForbidden
	}
	return owner, nil
}
func creationInput(in ports.SubscriptionCreationInput) (ports.SubscriptionCreationInput, error) {
	in = creationClone(in)
	if (in.MediaID == nil) == (in.TMDBID == nil) || (in.MediaID != nil && *in.MediaID == uuid.Nil) || (in.TMDBID != nil && *in.TMDBID <= 0) || (in.RootID != nil && *in.RootID == uuid.Nil) || (in.ExpectedIntentVersion != nil && *in.ExpectedIntentVersion < 1) {
		return in, ports.ErrInvalidSubscriptionBindings
	}
	if in.MediaType == "" && in.TMDBID != nil {
		in.MediaType = "movie"
	}
	if in.MediaType != "" && in.MediaType != "movie" && in.MediaType != "tv" {
		return in, ports.ErrInvalidSubscriptionBindings
	}
	if in.ScopeType == "" {
		in.ScopeType = "all"
	}
	if in.ScopeType != "all" && in.ScopeType != "selected" {
		return in, ports.ErrInvalidSubscriptionBindings
	}
	if in.QualityPolicyID == "" {
		in.QualityPolicyID = "default"
	}
	if len(in.QualityPolicyID) > 128 || strings.TrimSpace(in.QualityPolicyID) != in.QualityPolicyID {
		return in, ports.ErrInvalidSubscriptionBindings
	}
	if len(in.IndexerIDs) > 64 || len(in.Seasons) > 64 || len(in.EpisodeKeys) > 20000 || len(in.SelectedUnitIDs) > 20000 {
		return in, ports.ErrInvalidSubscriptionBindings
	}
	sort.Ints(in.Seasons)
	for i, n := range in.Seasons {
		if n < 0 || n > 999 || (n == 0 && !in.IncludeSpecials) || (i > 0 && in.Seasons[i-1] == n) {
			return in, ports.ErrInvalidSubscriptionBindings
		}
	}
	if !creationUUIDsValid(in.IndexerIDs) || !creationUUIDsValid(in.SelectedUnitIDs) || (in.DownloaderID != nil && *in.DownloaderID == uuid.Nil) {
		return in, ports.ErrInvalidSubscriptionBindings
	}
	sort.Strings(in.EpisodeKeys)
	for i, k := range in.EpisodeKeys {
		if k == "" || (i > 0 && in.EpisodeKeys[i-1] == k) {
			return in, ports.ErrInvalidSubscriptionBindings
		}
	}
	return in, nil
}
func creationUUIDsValid(ids []uuid.UUID) bool {
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// Receipt storage includes a version, action and fingerprint alongside the entire
// original response. Historical responses never derive from today's Wanted rows.
type creationStoredReceipt struct {
	Version     int                               `json:"version"`
	Action      string                            `json:"action"`
	RequestHash string                            `json:"request_hash"`
	ResultHash  string                            `json:"result_hash"`
	Result      ports.SubscriptionCreationReceipt `json:"result"`
}

func creationDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func creationStrictJSON(raw []byte, out any) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return false
	}
	canonical, err := json.Marshal(out)
	if err != nil {
		return false
	}
	var original, roundtrip any
	return json.Unmarshal(raw, &original) == nil && json.Unmarshal(canonical, &roundtrip) == nil && reflect.DeepEqual(original, roundtrip)
}
func creationReceipt(ctx context.Context, q *db.Queries, a *ports.Actor, key, hash string, owner *uuid.UUID) (*ports.SubscriptionCreationReceipt, error) {
	old, err := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var saved creationStoredReceipt
	if !creationStrictJSON(old.ResponseBody, &saved) {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	r := &saved.Result
	sub := r.Subscription
	if saved.Version != 1 || saved.Action != "subscription.creation" || !creationHash(old.RequestHash) || saved.RequestHash != old.RequestHash || saved.ResultHash != creationDigest(saved.Result) || old.ResponseCode != 201 || !old.ResourceID.Valid || old.ResourceID.String != sub.ID.String() || old.ID == uuid.Nil || !old.CreatedAt.Valid || old.ActorType != string(a.Type) || old.ActorID != a.ID || old.IdempotencyKey != key {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	// Authenticate the actual current object before returning even a valid old result.
	actual, err := q.GetSubscriptionByID(ctx, sub.ID)
	if err != nil {
		return nil, err
	}
	if !a.CanAccessUser(actual.UserID.String()) || (owner != nil && !a.CanAccessUser(owner.String())) {
		return nil, ports.ErrForbidden
	}
	if actual.UserID != sub.UserID || actual.MediaID != sub.MediaID || actual.RootID != sub.RootID || (owner != nil && *owner != sub.UserID) || (hash != "" && hash != old.RequestHash) || !creationRecordValid(sub) || r.Wanted == nil {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	seenIDs, seenUnits := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	units, err := q.ListMediaUnits(ctx, sub.MediaID)
	if err != nil {
		return nil, err
	}
	unitKeys := map[uuid.UUID]string{}
	for _, u := range units {
		unitKeys[u.ID] = u.UnitKey
	}
	// Validate immutable Wanted associations; return the stored historical flags.
	actualWanted, err := q.ListWantedItemsForSubscription(ctx, sub.ID)
	if err != nil {
		return nil, err
	}
	wantedUnits := map[uuid.UUID]uuid.UUID{}
	for _, w := range actualWanted {
		wantedUnits[w.ID] = w.UnitID
	}
	for _, w := range r.Wanted {
		if wantedUnits[w.ID] != w.UnitID {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		if w.ID == uuid.Nil || w.UnitID == uuid.Nil || w.UnitKey == "" || unitKeys[w.UnitID] != w.UnitKey || seenIDs[w.ID] || seenUnits[w.UnitID] {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		seenIDs[w.ID], seenUnits[w.UnitID] = true, true
	}
	return r, nil
}
func creationRecordValid(s ports.SubscriptionRecord) bool {
	if s.ID == uuid.Nil || s.UserID == uuid.Nil || s.MediaID == uuid.Nil || s.RootID == uuid.Nil || s.IntentVersion < 1 || s.QualityPolicyRevision < 1 || s.QualityPolicyID == "" || len(s.QualityPolicyID) > 128 || s.IndexerIDs == nil || s.Seasons == nil || s.SelectedUnitIDs == nil || !creationUUIDsValid(s.IndexerIDs) || !creationUUIDsValid(s.SelectedUnitIDs) || (s.DownloaderID != nil && *s.DownloaderID == uuid.Nil) || s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return false
	}
	if s.State != "active" && s.State != "paused" && s.State != "archived" {
		return false
	}
	if s.ScopeType != "all" && s.ScopeType != "selected" {
		return false
	}
	if s.UpgradeMode != "disabled" && s.UpgradeMode != "stop_at_cutoff" && s.UpgradeMode != "replace_if_better" {
		return false
	}
	if s.EpisodeMappingRevision != nil && *s.EpisodeMappingRevision < 1 {
		return false
	}
	seen := map[int]bool{}
	for _, n := range s.Seasons {
		if n < 0 || n > 999 || seen[n] || (n == 0 && !s.IncludeSpecials) {
			return false
		}
		seen[n] = true
	}
	return (s.ScopeType != "all" || len(s.SelectedUnitIDs) == 0) && (s.ScopeType != "selected" || len(s.SelectedUnitIDs) > 0)
}

func (s *SubscriptionCreationStore) ReadSubscriptionCreationReceipt(ctx context.Context, a *ports.Actor, key string) (out *ports.SubscriptionCreationReceipt, err error) {
	defer func() { err = creationError(ctx, err) }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	q := db.New(tx)
	actor := creationClone(a)
	if err = lockAcquisitionUser(ctx, q, actor); err != nil {
		return nil, err
	}
	key, err = creationKey(key)
	if err != nil {
		return nil, err
	}
	out, err = creationReceipt(ctx, q, actor, key, "", nil)
	if err == nil && out == nil {
		err = ports.ErrNotFound
	}
	return out, err
}
func (s *SubscriptionCreationStore) ReadSubscriptionCreationContext(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity) (out ports.SubscriptionCreationContext, receipt *ports.SubscriptionCreationReceipt, err error) {
	defer func() { err = creationError(ctx, err) }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, nil, err
	}
	defer creationRollback(ctx, tx)
	q := db.New(tx)
	actor := creationClone(a)
	if err = lockAcquisitionUser(ctx, q, actor); err != nil {
		return out, nil, err
	}
	owner, err := creationOwner(actor, in)
	if err != nil {
		return out, nil, err
	}
	key, err := creationKey(id.Key)
	if err != nil {
		return out, nil, err
	}
	if !creationHash(id.RequestHash) {
		return out, nil, ports.ErrInvalidSubscriptionBindings
	}
	receipt, err = creationReceipt(ctx, q, actor, key, id.RequestHash, &owner)
	if err != nil || receipt != nil {
		return out, receipt, err
	}
	in, err = creationInput(in)
	if err != nil {
		return out, nil, err
	}
	out, err = creationObserve(ctx, q, owner, in)
	return out, nil, err
}
func creationMedia(ctx context.Context, q *db.Queries, in ports.SubscriptionCreationInput) (db.Medium, int64, error) {
	var m db.Medium
	var err error
	var tmdb int64
	if in.MediaID != nil {
		m, err = q.GetMediaByID(ctx, *in.MediaID)
		if err != nil {
			return m, 0, err
		}
		identities, e := q.ListSearchMediaIdentities(ctx, m.ID)
		if e != nil {
			return m, 0, e
		}
		for _, identity := range identities {
			if identity.Namespace == "tmdb."+m.MediaType {
				tmdb, err = strconv.ParseInt(identity.ExternalID, 10, 64)
				if err != nil || tmdb <= 0 || strconv.FormatInt(tmdb, 10) != identity.ExternalID {
					return m, 0, ports.ErrSubscriptionBindingsConflict
				}
			}
		}
	} else {
		tmdb = *in.TMDBID
		identity, lookupErr := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "tmdb." + in.MediaType, ExternalID: strconv.FormatInt(tmdb, 10)})
		err = lookupErr
		if err == nil {
			m, err = q.GetMediaByID(ctx, identity.ID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Medium{MediaType: in.MediaType}, tmdb, nil
		}
		if err != nil {
			return m, 0, err
		}
	}
	if (m.MediaType != "movie" && m.MediaType != "tv") || (in.MediaType != "" && m.MediaType != in.MediaType) {
		return m, 0, ports.ErrInvalidSubscriptionBindings
	}
	return m, tmdb, nil
}
func creationRoot(ctx context.Context, q *db.Queries, in ports.SubscriptionCreationInput, kind string) (db.LibraryRoot, error) {
	var id uuid.UUID
	if in.RootID != nil {
		id = *in.RootID
	} else {
		roots, err := q.ListConfiguredLibraryRootSelections(ctx)
		if err != nil {
			return db.LibraryRoot{}, err
		}
		for _, r := range roots {
			if r.MediaType == kind && r.Health == "online" {
				if id != uuid.Nil {
					return db.LibraryRoot{}, ports.ErrAmbiguousSubscriptionRoot
				}
				id = r.ID
			}
		}
	}
	if id == uuid.Nil {
		return db.LibraryRoot{}, ports.ErrNoSubscriptionRoot
	}
	r, err := q.GetLibraryRootByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		if in.RootID != nil {
			return r, ports.ErrNotFound
		}
		return r, ports.ErrNoSubscriptionRoot
	}
	if err != nil {
		return r, err
	}
	if r.MediaType != kind || r.Health != "online" {
		return r, ports.ErrNoSubscriptionRoot
	}
	return r, nil
}
func creationObserve(ctx context.Context, q *db.Queries, owner uuid.UUID, in ports.SubscriptionCreationInput) (out ports.SubscriptionCreationContext, err error) {
	m, tmdb, err := creationMedia(ctx, q, in)
	if err != nil {
		return out, err
	}
	root, err := creationRoot(ctx, q, in, m.MediaType)
	if err != nil {
		return out, err
	}
	policy, err := q.GetPolicyByID(ctx, in.QualityPolicyID)
	if err != nil {
		return out, err
	}
	out = ports.SubscriptionCreationContext{OwnerID: owner, MediaID: m.ID, MediaType: m.MediaType, TMDBID: tmdb, MediaVersion: m.MetadataVersion, RootID: root.ID, RootGeneration: root.Generation, RootHealthRevision: root.HealthRevision, PolicyRevision: policy.CurrentRevision}
	if m.ID != uuid.Nil {
		sub, e := q.GetSubscriptionByUserMediaRoot(ctx, db.GetSubscriptionByUserMediaRootParams{UserID: owner, MediaID: m.ID, RootID: root.ID})
		if e == nil {
			record, e := subscriptionIntentRecord(sub)
			if e != nil {
				return out, e
			}
			out.Existing = &record
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
	}
	return out, nil
}

func (s *SubscriptionCreationStore) CommitSubscriptionCreation(ctx context.Context, a *ports.Actor, in ports.SubscriptionCreationInput, hint ports.SubscriptionCreationContext, prepared *ports.PreparedSubscriptionCreationMetadata, id ports.SubscriptionCreationIdentity, decide ports.SubscriptionCreationDecider) (out *ports.SubscriptionCreationReceipt, err error) {
	defer func() { err = creationError(ctx, err) }()
	// Independent copies survive arbitrary mutations by a callback of its input,
	// caller-owned request, provider material, actor, or pre-I/O observation.
	actor := creationClone(a)
	in = creationClone(in)
	hint = creationClone(hint)
	preparedRaw, preparedErr := json.Marshal(prepared)
	prepared = nil
	if preparedErr == nil {
		preparedErr = json.Unmarshal(preparedRaw, &prepared)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer creationRollback(ctx, tx)
	q := db.New(tx)
	t := &subscriptionIntentTx{tx: tx, q: q, ids: s.ids, actor: actor}
	if err = lockAcquisitionUser(ctx, q, actor); err != nil {
		return nil, err
	}
	owner, err := creationOwner(actor, in)
	if err != nil {
		return nil, err
	}
	key, err := creationKey(id.Key)
	if err != nil {
		return nil, err
	}
	if !creationHash(id.RequestHash) {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	commandLock, _ := json.Marshal([]string{string(actor.Type), actor.ID, key})
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription.creation", AggregateID: string(commandLock)}); err != nil {
		return nil, err
	}
	if old, e := creationReceipt(ctx, q, actor, key, id.RequestHash, &owner); e != nil || old != nil {
		return old, e
	}
	if preparedErr != nil {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	in, err = creationInput(in)
	if err != nil {
		return nil, err
	}
	if decide == nil {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	observed, err := creationObserve(ctx, q, owner, in)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(observed, hint) {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	if err = validateCreationMetadata(prepared, observed, in); err != nil {
		return nil, err
	}
	subs := []db.Subscription{}
	if observed.MediaID != uuid.Nil {
		subs, err = q.ListTVMediaSubscriptions(ctx, observed.MediaID)
		if err != nil {
			return nil, err
		}
	}
	policies := map[string]bool{in.QualityPolicyID: true}
	for _, sub := range subs {
		policies[sub.QualityPolicyID] = true
	}
	keys := make([]string, 0, len(policies))
	for p := range policies {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	var revision int64
	for _, p := range keys {
		row, e := q.LockSearchPolicy(ctx, p)
		if e != nil {
			return nil, e
		}
		if p == in.QualityPolicyID {
			revision = row.CurrentRevision
		}
	}
	if revision != hint.PolicyRevision {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	// Legacy LockCreate takes policy before this identity lock. Match its order
	// and recheck identity discovery after the potentially blocking acquisition.
	if observed.TMDBID > 0 {
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))", "tmdb."+observed.MediaType, strconv.FormatInt(observed.TMDBID, 10)); err != nil {
			return nil, err
		}
	}
	currentIdentity, currentTMDB, err := creationMedia(ctx, q, in)
	if err != nil {
		return nil, err
	}
	if currentIdentity.ID != observed.MediaID || currentIdentity.MediaType != observed.MediaType || currentTMDB != observed.TMDBID {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "subscription.intent", AggregateID: owner.String() + ":" + observed.MediaID.String() + ":" + observed.RootID.String()}); err != nil {
		return nil, err
	}
	var existing *db.Subscription
	for i, sub := range subs {
		locked, e := q.LockSearchSubscription(ctx, sub.ID)
		if e != nil {
			return nil, e
		}
		if locked.QualityPolicyID != sub.QualityPolicyID || locked.RootID != sub.RootID {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		subs[i] = locked
		if locked.UserID == owner && locked.RootID == observed.RootID {
			v := locked
			existing = &v
		}
	}
	// Root registration and every matching root's current health participate in
	// inferred selection, including offline roots that could become eligible.
	if err = q.LockLibraryRootBootstrap(ctx); err != nil {
		return nil, err
	}
	roots, err := q.ListConfiguredLibraryRootSelections(ctx)
	if err != nil {
		return nil, err
	}
	rootSet := map[uuid.UUID]bool{observed.RootID: true}
	for _, sub := range subs {
		rootSet[sub.RootID] = true
	}
	if in.RootID == nil {
		for _, r := range roots {
			if r.MediaType == observed.MediaType {
				rootSet[r.ID] = true
			}
		}
	}
	rootIDs := make([]uuid.UUID, 0, len(rootSet))
	for r := range rootSet {
		rootIDs = append(rootIDs, r)
	}
	sort.Slice(rootIDs, func(i, j int) bool { return rootIDs[i].String() < rootIDs[j].String() })
	for _, r := range rootIDs {
		if _, err = q.LockAcquisitionRoot(ctx, r); err != nil {
			return nil, err
		}
	}
	root, err := creationRoot(ctx, q, in, observed.MediaType)
	if err != nil {
		return nil, err
	}
	if root.ID != hint.RootID || root.Generation != hint.RootGeneration || root.HealthRevision != hint.RootHealthRevision {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	mediaID := observed.MediaID
	if mediaID == uuid.Nil {
		mediaID, err = t.createCreationMedia(ctx, prepared)
		if err != nil {
			return nil, err
		}
	} else {
		if _, err = q.LockScopeMediaUnits(ctx, mediaID); err != nil {
			return nil, err
		}
		m, e := q.LockSubmissionMedia(ctx, mediaID)
		if e != nil {
			return nil, e
		}
		if m.MetadataVersion != hint.MediaVersion || m.MediaType != hint.MediaType {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		current, e := q.ListTVMediaSubscriptions(ctx, mediaID)
		if e != nil {
			return nil, e
		}
		if len(current) != len(subs) {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		for i := range current {
			if current[i].ID != subs[i].ID || current[i].IntentVersion != subs[i].IntentVersion {
				return nil, ports.ErrSubscriptionBindingsConflict
			}
		}
	}
	if existing == nil && hint.Existing != nil {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	if existing != nil {
		record, e := subscriptionIntentRecord(*existing)
		if e != nil {
			return nil, e
		}
		if hint.Existing == nil || !reflect.DeepEqual(record, *hint.Existing) {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
	}
	if prepared != nil && observed.MediaType == "tv" {
		if err = t.mergeCreationTV(ctx, mediaID, observed.TMDBID, prepared.Seasons, subs); err != nil {
			return nil, err
		}
	}
	units, err := q.LockScopeMediaUnits(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	locked := ports.SubscriptionCreateContext{MediaID: mediaID, MediaType: observed.MediaType, RootMediaType: root.MediaType, RootHealth: root.Health, PolicyRevision: revision, Units: make([]ports.SubscriptionIntentUnit, 0, len(units))}
	if existing != nil {
		record, e := subscriptionIntentRecord(*existing)
		if e != nil {
			return nil, e
		}
		locked.Existing = &record
		if in.ExpectedIntentVersion == nil && record.QualityPolicyID == in.QualityPolicyID {
			locked.PolicyRevision = record.QualityPolicyRevision
		}
	}
	for _, u := range units {
		locked.Units = append(locked.Units, ports.SubscriptionIntentUnit{ID: u.ID, UnitKey: u.UnitKey, SeasonNumber: intentInt4(u.SeasonNumber), EpisodeNumber: intentInt4(u.EpisodeNumber), Retired: u.Retired})
	}
	frozenMedia, err := q.GetMediaByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	expected, err := creationExpected(in, locked, root.ID)
	if err != nil {
		return nil, err
	}
	decision, err := decide(creationClone(locked))
	if err != nil {
		return nil, err
	}
	if !creationDecisionEqual(decision, expected) {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	if err = lockAcquisitionUser(ctx, q, actor); err != nil {
		return nil, err
	}
	if _, err = creationOwner(actor, in); err != nil {
		return nil, err
	}
	// SQL held row locks across the pure callback. Verify its independent frozen
	// bindings and the actual media/unit versions before publishing the intent.
	currentMedia, err := q.GetMediaByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(currentMedia, frozenMedia) {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	currentUnits, err := q.LockScopeMediaUnits(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(currentUnits, units) {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	if existing != nil {
		current, e := q.GetSubscriptionByID(ctx, existing.ID)
		if e != nil {
			return nil, e
		}
		if !reflect.DeepEqual(current, *existing) {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
	}
	sub, err := t.publishCreationIntent(ctx, in, expected, existing)
	if err != nil {
		return nil, err
	}
	record, err := subscriptionIntentRecord(sub)
	if err != nil {
		return nil, err
	}
	if record.IndexerIDs == nil {
		record.IndexerIDs = []uuid.UUID{}
	}
	if record.Seasons == nil {
		record.Seasons = []int{}
	}
	if record.SelectedUnitIDs == nil {
		record.SelectedUnitIDs = []uuid.UUID{}
	}
	out = &ports.SubscriptionCreationReceipt{Subscription: record, Wanted: make([]ports.SubscriptionCreationWanted, 0)}
	wanted, err := q.ListWantedItemsForSubscription(ctx, sub.ID)
	if err != nil {
		return nil, err
	}
	unitKeys := map[uuid.UUID]string{}
	for _, u := range units {
		unitKeys[u.ID] = u.UnitKey
	}
	for _, w := range wanted {
		k, ok := unitKeys[w.UnitID]
		if !ok {
			return nil, ports.ErrSubscriptionBindingsConflict
		}
		out.Wanted = append(out.Wanted, ports.SubscriptionCreationWanted{ID: w.ID, UnitID: w.UnitID, UnitKey: k, IsSkipped: w.IsSkipped, IsSatisfied: w.IsSatisfied})
	}
	sort.Slice(out.Wanted, func(i, j int) bool { return out.Wanted[i].UnitKey < out.Wanted[j].UnitKey })
	payload := map[string]any{"subscription_id": sub.ID, "intent_version": sub.IntentVersion, "wanted_count": len(out.Wanted)}
	audit, _ := json.Marshal(payload)
	if err = t.audit(ctx, sub.ID, "subscription.creation", audit); err != nil {
		return nil, err
	}
	event := "subscription.creation_ensured"
	if existing == nil {
		event = "subscription.created"
	} else if in.ExpectedIntentVersion != nil {
		event = "subscription.updated"
	}
	if err = t.publish(ctx, sub.ID, event, payload); err != nil {
		return nil, err
	}
	stored := creationStoredReceipt{Version: 1, Action: "subscription.creation", RequestHash: id.RequestHash, ResultHash: creationDigest(out), Result: *out}
	raw, _ := json.Marshal(stored)
	if _, err = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(actor.Type), ActorID: actor.ID, IdempotencyKey: key, RequestHash: id.RequestHash, ResponseCode: 201, ResponseBody: raw, ResourceID: pgtype.Text{String: sub.ID.String(), Valid: true}}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func creationExpected(in ports.SubscriptionCreationInput, c ports.SubscriptionCreateContext, root uuid.UUID) (ports.SubscriptionCreateDecision, error) {
	cmd := ports.CreateSubscriptionCommand{MediaID: c.MediaID, RootID: root, QualityPolicyID: in.QualityPolicyID, QualityPolicyRevision: c.PolicyRevision, IndexerIDs: in.IndexerIDs, DownloaderID: in.DownloaderID, ScopeType: in.ScopeType, Seasons: in.Seasons, IncludeSpecials: in.IncludeSpecials, EpisodeKeys: in.EpisodeKeys, SelectedUnitIDs: in.SelectedUnitIDs, UserID: in.UserID}
	if c.Existing != nil {
		if in.IndexerIDs == nil {
			cmd.IndexerIDs = c.Existing.IndexerIDs
		}
		if in.DownloaderID == nil {
			cmd.DownloaderID = c.Existing.DownloaderID
		}
	}
	if cmd.IndexerIDs == nil {
		cmd.IndexerIDs = []uuid.UUID{}
	}
	if cmd.Seasons == nil {
		cmd.Seasons = []int{}
	}
	if cmd.SelectedUnitIDs == nil {
		cmd.SelectedUnitIDs = []uuid.UUID{}
	}
	selected := map[uuid.UUID]bool{}
	for _, id := range cmd.SelectedUnitIDs {
		selected[id] = true
	}
	keys := map[string]bool{}
	for _, k := range in.EpisodeKeys {
		keys[k] = true
	}
	available := map[uuid.UUID]bool{}
	base := domain.Scope{Type: "all", Seasons: cmd.Seasons, IncludeSpecials: cmd.IncludeSpecials}
	for _, u := range c.Units {
		if u.ID == uuid.Nil || u.Retired {
			continue
		}
		if c.MediaType == "movie" {
			if u.UnitKey != media.FormatMovieUnitKey() || u.SeasonNumber != nil || u.EpisodeNumber != nil {
				continue
			}
		} else {
			if u.SeasonNumber == nil || u.EpisodeNumber == nil || *u.SeasonNumber < 0 || *u.SeasonNumber > 999 || *u.EpisodeNumber < 1 || *u.EpisodeNumber > 9999 || u.UnitKey != media.FormatEpisodeUnitKey(int(*u.SeasonNumber), int(*u.EpisodeNumber)) {
				continue
			}
		}
		season := 0
		if u.SeasonNumber != nil {
			season = int(*u.SeasonNumber)
		}
		if !domain.ScopeContains(base, domain.ScopeUnit{ID: u.ID, Season: season, TV: c.MediaType == "tv", Retired: u.Retired}) {
			continue
		}
		available[u.ID] = true
		if keys[u.UnitKey] {
			if !selected[u.ID] {
				cmd.SelectedUnitIDs = append(cmd.SelectedUnitIDs, u.ID)
			}
			selected[u.ID] = true
			delete(keys, u.UnitKey)
		}
	}
	if len(keys) > 0 || (cmd.ScopeType == "all" && len(selected) > 0) || (cmd.ScopeType == "selected" && len(selected) == 0) {
		return ports.SubscriptionCreateDecision{}, ports.ErrInvalidSubscriptionBindings
	}
	for id := range selected {
		if !available[id] {
			return ports.SubscriptionCreateDecision{}, ports.ErrInvalidSubscriptionBindings
		}
	}
	if c.MediaType == "movie" && (cmd.IncludeSpecials || len(cmd.Seasons) > 0 || len(in.EpisodeKeys) > 0) {
		return ports.SubscriptionCreateDecision{}, ports.ErrInvalidSubscriptionBindings
	}
	d := ports.SubscriptionCreateDecision{Command: cmd, Wanted: make([]ports.SubscriptionWantedDecision, 0, len(available))}
	for _, u := range c.Units {
		if available[u.ID] {
			d.Wanted = append(d.Wanted, ports.SubscriptionWantedDecision{UnitID: u.ID, ScopeExcluded: cmd.ScopeType == "selected" && !selected[u.ID]})
		}
	}
	if c.MediaType == "movie" && len(available) == 0 {
		return d, ports.ErrInvalidSubscriptionBindings
	}
	return d, nil
}
func creationDecisionEqual(a, b ports.SubscriptionCreateDecision) bool {
	if a.Command.MediaInput != nil {
		return false
	}
	normalize := func(c *ports.CreateSubscriptionCommand) {
		c.IndexerIDs = slices.Clone(c.IndexerIDs)
		c.Seasons = slices.Clone(c.Seasons)
		c.SelectedUnitIDs = slices.Clone(c.SelectedUnitIDs)
		c.EpisodeKeys = slices.Clone(c.EpisodeKeys)
		sort.Ints(c.Seasons)
		sort.Slice(c.SelectedUnitIDs, func(i, j int) bool { return c.SelectedUnitIDs[i].String() < c.SelectedUnitIDs[j].String() })
		sort.Strings(c.EpisodeKeys)
		if len(c.EpisodeKeys) == 0 {
			c.EpisodeKeys = nil
		}
	}
	normalize(&a.Command)
	normalize(&b.Command)
	if !reflect.DeepEqual(a.Command, b.Command) {
		return false
	}
	if len(a.Wanted) != len(b.Wanted) {
		return false
	}
	want := map[uuid.UUID]bool{}
	for _, w := range b.Wanted {
		want[w.UnitID] = w.ScopeExcluded
	}
	for _, w := range a.Wanted {
		excluded, ok := want[w.UnitID]
		if !ok || excluded != w.ScopeExcluded {
			return false
		}
		delete(want, w.UnitID)
	}
	return len(want) == 0
}
func (t *subscriptionIntentTx) publishCreationIntent(ctx context.Context, in ports.SubscriptionCreationInput, d ports.SubscriptionCreateDecision, existing *db.Subscription) (sub db.Subscription, err error) {
	c := d.Command
	for _, id := range c.IndexerIDs {
		if _, err = t.q.GetIndexerByID(ctx, id); err != nil {
			return sub, err
		}
	}
	if c.DownloaderID != nil {
		if _, err = t.q.GetDownloadClientByID(ctx, *c.DownloaderID); err != nil {
			return sub, err
		}
	}
	indexers, _ := json.Marshal(c.IndexerIDs)
	seasons, _ := json.Marshal(c.Seasons)
	selected, _ := json.Marshal(c.SelectedUnitIDs)
	var dl pgtype.UUID
	if c.DownloaderID != nil {
		dl = pgtype.UUID{Bytes: *c.DownloaderID, Valid: true}
	}
	if existing == nil {
		if in.ExpectedIntentVersion != nil {
			return sub, ports.ErrSubscriptionBindingsConflict
		}
		owner, _ := creationOwner(t.actor, in)
		sub, err = t.q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: t.ids.NewUUID(), UserID: owner, MediaID: c.MediaID, RootID: c.RootID, QualityPolicyID: c.QualityPolicyID, QualityPolicyRevision: c.QualityPolicyRevision, IndexerIds: indexers, DownloaderID: dl, ScopeType: c.ScopeType})
		if err == nil {
			sub, err = t.q.InitializeSubscriptionTVScope(ctx, db.InitializeSubscriptionTVScopeParams{ID: sub.ID, Seasons: seasons, IncludeSpecials: c.IncludeSpecials})
		}
	} else if in.ExpectedIntentVersion == nil {
		record, e := subscriptionIntentRecord(*existing)
		if e != nil {
			return sub, e
		}
		effective := ports.CreateSubscriptionCommand{MediaID: record.MediaID, RootID: record.RootID, QualityPolicyID: record.QualityPolicyID, QualityPolicyRevision: record.QualityPolicyRevision, IndexerIDs: record.IndexerIDs, DownloaderID: record.DownloaderID, ScopeType: record.ScopeType, Seasons: record.Seasons, SelectedUnitIDs: record.SelectedUnitIDs, IncludeSpecials: record.IncludeSpecials, UserID: c.UserID, EpisodeKeys: c.EpisodeKeys}
		if !creationDecisionEqual(ports.SubscriptionCreateDecision{Command: c}, ports.SubscriptionCreateDecision{Command: effective}) {
			return sub, ports.ErrSubscriptionBindingsConflict
		}
		sub = *existing
	} else {
		if *in.ExpectedIntentVersion != existing.IntentVersion {
			return sub, ports.ErrSubscriptionBindingsConflict
		}
		sub, err = t.q.ReconfigureSubscription(ctx, db.ReconfigureSubscriptionParams{ID: existing.ID, QualityPolicyID: c.QualityPolicyID, QualityPolicyRevision: c.QualityPolicyRevision, IndexerIds: indexers, DownloaderID: dl, ScopeType: c.ScopeType, Seasons: seasons, IncludeSpecials: c.IncludeSpecials})
	}
	if err != nil {
		return sub, err
	}
	if existing == nil || in.ExpectedIntentVersion != nil {
		if err = t.q.StoreSubscriptionSelectedUnits(ctx, db.StoreSubscriptionSelectedUnitsParams{ID: sub.ID, SelectedUnitIds: selected}); err != nil {
			return sub, err
		}
		sub.SelectedUnitIds = selected
		if err = t.q.SkipWantedBeforeReconfiguration(ctx, db.SkipWantedBeforeReconfigurationParams{SubscriptionID: sub.ID, IntentVersion: sub.IntentVersion}); err != nil {
			return sub, err
		}
	}
	for _, w := range d.Wanted {
		if _, err = t.q.ApplyScopeWanted(ctx, db.ApplyScopeWantedParams{ID: t.ids.NewUUID(), SubscriptionID: sub.ID, UnitID: w.UnitID, IntentVersion: sub.IntentVersion, IsSkipped: w.ScopeExcluded}); err != nil {
			return sub, err
		}
	}
	return sub, nil
}
