package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	sd "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AutomaticTVRoundStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewAutomaticTVRoundStore(pool *pgxpool.Pool, ids ports.IDGen) *AutomaticTVRoundStore {
	return &AutomaticTVRoundStore{pool: pool, ids: ids}
}

var _ ports.AutomaticTVRoundRepository = (*AutomaticTVRoundStore)(nil)

type automaticTVRoundRow struct {
	ID             uuid.UUID                      `json:"id"`
	WantedID       uuid.UUID                      `json:"wanted_id"`
	SubscriptionID uuid.UUID                      `json:"subscription_id"`
	OwnerID        uuid.UUID                      `json:"owner_id"`
	Snapshot       ports.AutomaticTVRoundSnapshot `json:"snapshot"`
	SnapshotHash   string                         `json:"snapshot_hash"`
	FinalizeJobID  uuid.UUID                      `json:"finalize_job_id"`
	CreatedAt      time.Time                      `json:"created_at"`
	Deadline       time.Time                      `json:"deadline"`
	State          string                         `json:"state"`
}
type automaticTVSourceRow struct {
	RoundID        uuid.UUID            `json:"round_id"`
	IndexerID      uuid.UUID            `json:"indexer_id"`
	ConfigVersion  int64                `json:"config_version"`
	JobID          uuid.UUID            `json:"job_id"`
	Parameters     ports.TVEpisodeQuery `json:"parameters"`
	ParametersHash string               `json:"parameters_hash"`
	State          string               `json:"state"`
}
type automaticTVPage struct {
	Page           int32                `json:"page"`
	QueryID        uuid.UUID            `json:"query_id"`
	SourceJobID    uuid.UUID            `json:"source_job_id"`
	SourceToken    int64                `json:"source_token"`
	Parameters     ports.TVEpisodeQuery `json:"parameters"`
	ParametersHash string               `json:"parameters_hash"`
}

func automaticTVError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, allowed := range []error{ports.ErrRejected, ports.ErrAcquisitionRoundAborted, ports.ErrAutomaticTVSearchDeferred} {
		if errors.Is(err, allowed) {
			return allowed
		}
	}
	return ports.ErrUnavailable
}
func automaticTVDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// This mirrors the typed port canonical form without importing the usecase layer.
func canonicalAutomaticTVQuery(v ports.TVEpisodeQuery) (ports.TVEpisodeQuery, []byte, string, error) {
	if v.SchemaVersion == "" {
		v.SchemaVersion = "tv-episode-query-v1"
	}
	if v.Mode == "" {
		v.Mode = "tvsearch"
	}
	if v.SeriesNamespace == "" {
		v.SeriesNamespace = "tmdb.tv"
	}
	if v.SchemaVersion != "tv-episode-query-v1" || v.Mode != "tvsearch" || v.SeriesNamespace != "tmdb.tv" || v.SeriesID <= 0 || !utf8.ValidString(v.Keyword) || strings.ContainsFunc(v.Keyword, unicode.IsControl) {
		return v, nil, "", ports.ErrBadResponse
	}
	v.Keyword = strings.TrimSpace(v.Keyword)
	if v.Keyword == "" || len(v.Keyword) > 512 || v.Season < 1 || v.Season > 999 || v.Episode < 1 || v.Episode > 9999 || v.Categories == nil || len(v.Categories) > 100 || v.Offset < 0 || v.Offset > 150 || v.Offset%50 != 0 || v.Limit != 50 {
		return v, nil, "", ports.ErrBadResponse
	}
	v.Categories = append(make([]int32, 0, len(v.Categories)), v.Categories...)
	slices.Sort(v.Categories)
	for i, c := range v.Categories {
		if c <= 0 || (i > 0 && c == v.Categories[i-1]) {
			return v, nil, "", ports.ErrBadResponse
		}
	}
	raw, e := json.Marshal(v)
	if e != nil {
		return v, nil, "", ports.ErrBadResponse
	}
	return v, raw, automaticTVDigest(raw), nil
}
func automaticTVSnapshotBytes(v ports.AutomaticTVRoundSnapshot) ([]byte, string, error) {
	v.AirDate = v.AirDate.UTC()
	v.ObservedAt = v.ObservedAt.UTC()
	var rules any
	decoder := json.NewDecoder(strings.NewReader(string(v.Rules)))
	decoder.UseNumber()
	if decoder.Decode(&rules) != nil {
		return nil, "", ports.ErrUnavailable
	}
	v.Rules, _ = json.Marshal(rules)
	raw, e := json.Marshal(v)
	return raw, automaticTVDigest(raw), e
}
func automaticTVDecodeRound(raw []byte, e error) (*automaticTVRoundRow, error) {
	if e != nil {
		return nil, e
	}
	var r automaticTVRoundRow
	if json.Unmarshal(raw, &r) != nil {
		return nil, ports.ErrUnavailable
	}
	_, hash, e := automaticTVSnapshotBytes(r.Snapshot)
	if e != nil || hash != r.SnapshotHash || r.ID == uuid.Nil || r.WantedID != r.Snapshot.WantedID || r.SubscriptionID != r.Snapshot.SubscriptionID || r.OwnerID != r.Snapshot.OwnerID || !r.CreatedAt.Equal(r.Snapshot.ObservedAt) {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	return &r, nil
}
func automaticTVDecodeSource(raw []byte, e error) (*automaticTVSourceRow, error) {
	if e != nil {
		return nil, e
	}
	var s automaticTVSourceRow
	if json.Unmarshal(raw, &s) != nil {
		return nil, ports.ErrUnavailable
	}
	v, _, hash, e := canonicalAutomaticTVQuery(s.Parameters)
	if e != nil || hash != s.ParametersHash || v.Offset != 0 {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	s.Parameters = v
	return &s, nil
}

// The account is always acquired before policy/subscription/root/unit locks.
func automaticTVLockOwner(ctx context.Context, q *db.Queries, id uuid.UUID) (*ports.Actor, error) {
	u, e := q.LockAcquisitionCommandUser(ctx, id)
	if e != nil {
		return nil, e
	}
	if !u.IsActive || (u.Role != ports.RoleMember && u.Role != ports.RoleAdmin) {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	return &ports.Actor{Type: ports.ActorTypeUser, ID: id.String(), Role: u.Role}, nil
}

// Called only under current account and policy/subscription locks. No external I/O.
func automaticTVSnapshot(ctx context.Context, q *db.Queries, sub db.Subscription, wantedID uuid.UUID, observed time.Time) (ports.AutomaticTVRoundSnapshot, map[uuid.UUID]ports.TVEpisodeQuery, error) {
	var snap ports.AutomaticTVRoundSnapshot
	reject := func() (ports.AutomaticTVRoundSnapshot, map[uuid.UUID]ports.TVEpisodeQuery, error) {
		return snap, nil, ports.ErrAcquisitionRoundAborted
	}
	if sub.State != "active" || !sub.DownloaderID.Valid || sub.UpgradeMode != "disabled" {
		return reject()
	}
	root, e := q.LockAcquisitionRoot(ctx, sub.RootID)
	if e != nil {
		return snap, nil, e
	}
	if root.MediaType != "tv" || root.Health != "online" || !root.BaselineCompleted {
		return reject()
	}
	hint, e := q.GetWantedItemByID(ctx, wantedID)
	if e != nil {
		return snap, nil, e
	}
	unit, e := q.LockAcquisitionUnit(ctx, hint.UnitID)
	if e != nil {
		return snap, nil, e
	}
	w, e := q.LockSearchWanted(ctx, wantedID)
	if e != nil {
		return snap, nil, e
	}
	m, e := q.LockSearchMedia(ctx, sub.MediaID)
	if e != nil {
		return snap, nil, e
	}
	if w.SubscriptionID != sub.ID || w.UnitID != unit.ID || w.IsSkipped || w.UserSkipped || w.ScopeExcluded || w.IntentVersion != sub.IntentVersion || unit.MediaID != m.ID || unit.Retired || unit.MetadataVersion < 1 || m.MediaType != "tv" || m.MetadataVersion < 1 || !unit.SeasonNumber.Valid || !unit.EpisodeNumber.Valid || unit.AbsoluteNumber.Valid || unit.SeasonNumber.Int32 < 1 || unit.EpisodeNumber.Int32 < 1 || unit.UnitKey != fmt.Sprintf("S%02dE%02d", unit.SeasonNumber.Int32, unit.EpisodeNumber.Int32) || !unit.AirDate.Valid {
		return reject()
	}
	aired, e := q.AutomaticTVAired(ctx, unit.ID)
	if e != nil {
		return snap, nil, e
	}
	if !aired.Valid || !aired.Bool {
		return reject()
	}
	ids, e := q.ListSearchMediaIdentities(ctx, m.ID)
	if e != nil {
		return snap, nil, e
	}
	var tmdb int64
	for _, i := range ids {
		if i.Namespace == "tmdb.tv" {
			tmdb, e = strconv.ParseInt(i.ExternalID, 10, 64)
			if e != nil {
				return reject()
			}
		}
	}
	if tmdb <= 0 {
		return reject()
	}
	episode, e := q.GetTMDBEpisodeIdentityForUnit(ctx, unit.ID)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return reject()
		}
		return snap, nil, e
	}
	if episode.ExternalID <= 0 || episode.TmdbSeriesID != tmdb {
		return reject()
	}
	mode := "standard"
	revision := sub.EpisodeMappingRevision.Int64
	if sub.EpisodeMappingRevision.Valid {
		mapping, e := q.GetCurrentEpisodeMapping(ctx, sub.ID)
		if e != nil {
			return snap, nil, e
		}
		if mapping.Mode != "standard" || mapping.MediaID != m.ID || mapping.MediaVersion != m.MetadataVersion {
			return reject()
		}
		mode = mapping.Mode
	}
	policy, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: sub.QualityPolicyID, Revision: sub.QualityPolicyRevision})
	if e != nil {
		return snap, nil, e
	}
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: sub.UserID.String(), Role: ports.RoleMember}
	status, e := readSubscriptionStatus(ctx, q, actor, sub.ID, &observed)
	if e != nil {
		return snap, nil, e
	}
	eligible := false
	for _, v := range sd.DeriveProjection(status).Items {
		if v.ID == w.ID {
			eligible = v.Status == "missing" && !v.IsSatisfied && !v.ReservationProtected && (v.WaitReason == "missing" || v.WaitReason == "no_qualified_candidate")
		}
	}
	if !eligible {
		return reject()
	}
	blocked, e := q.AutomaticTVBlocked(ctx, unit.ID)
	if e != nil {
		return snap, nil, e
	}
	if blocked {
		return reject()
	}
	var bound []uuid.UUID
	if json.Unmarshal(sub.IndexerIds, &bound) != nil || len(bound) == 0 || len(bound) > 64 {
		return reject()
	}
	slices.SortFunc(bound, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	sources := map[uuid.UUID]ports.TVEpisodeQuery{}
	versions := map[string]int64{}
	keyword := strings.TrimSpace(m.Title)
	if m.Year.Valid && m.Year.Int32 > 0 {
		keyword = fmt.Sprintf("%s %d", keyword, m.Year.Int32)
	}
	for pos, id := range bound {
		if id == uuid.Nil || (pos > 0 && id == bound[pos-1]) {
			return reject()
		}
		idx, e := q.LockSearchIndexer(ctx, id)
		if e != nil {
			return snap, nil, e
		}
		current, e := q.AutomaticTVSourceCurrent(ctx, db.AutomaticTVSourceCurrentParams{ID: id, ConfigVersion: idx.ConfigVersion})
		if e != nil {
			return snap, nil, e
		}
		if !current {
			continue
		}
		var categories []int32
		if json.Unmarshal(idx.Categories, &categories) != nil {
			return snap, nil, ports.ErrUnavailable
		}
		query, _, _, e := canonicalAutomaticTVQuery(ports.TVEpisodeQuery{SeriesID: tmdb, Keyword: keyword, Season: unit.SeasonNumber.Int32, Episode: unit.EpisodeNumber.Int32, Categories: categories, Limit: 50})
		if e != nil {
			return snap, nil, e
		}
		sources[id] = query
		versions[id.String()] = idx.ConfigVersion
	}
	if len(sources) == 0 {
		return reject()
	}
	clientID := uuid.UUID(sub.DownloaderID.Bytes)
	if _, e = q.GetSearchDownloaderSnapshot(ctx, clientID); e != nil {
		return snap, nil, e
	}
	client, e := q.GetDownloadClientByID(ctx, clientID)
	if e != nil {
		return snap, nil, e
	}
	var pathmap map[string]string
	if (client.ClientType != "qbittorrent" && client.ClientType != "transmission") || json.Unmarshal(client.RemotePathMap, &pathmap) != nil || naming.TranslateRemotePath(pathmap, root.Path) == "" {
		return reject()
	}
	snap = ports.AutomaticTVRoundSnapshot{SchemaVersion: "automatic-tv-round-v1", IdentityRuleVersion: media.AutomaticTVIdentityRuleVersion, ParserVersion: media.EpisodeParserVersion, SubscriptionID: sub.ID, OwnerID: sub.UserID, WantedID: w.ID, IntentVersion: sub.IntentVersion, PolicyID: sub.QualityPolicyID, PolicyRevision: sub.QualityPolicyRevision, Rules: append(json.RawMessage(nil), policy.Rules...), MediaID: m.ID, MediaVersion: m.MetadataVersion, TMDBID: tmdb, Title: m.Title, OriginalTitle: m.OriginalTitle.String, Year: m.Year.Int32, UnitID: unit.ID, UnitKey: unit.UnitKey, UnitVersion: unit.MetadataVersion, EpisodeID: episode.ExternalID, Season: unit.SeasonNumber.Int32, Episode: unit.EpisodeNumber.Int32, AirDate: unit.AirDate.Time.UTC(), MappingMode: mode, MappingRevision: revision, RootID: root.ID, RootGeneration: root.Generation, RootHealthRevision: root.HealthRevision, ClientID: clientID, ClientStamp: episodeClientStamp(client), IndexerVersions: versions, ObservedAt: observed.UTC()}
	return snap, sources, nil
}

func (s *AutomaticTVRoundStore) StartAutomaticTVRound(ctx context.Context, in ports.AutomaticTVRoundStart) (out *ports.AutomaticTVRound, err error) {
	defer func() {
		if !errors.Is(err, ports.ErrAutomaticTVNotApplicable) {
			err = automaticTVError(ctx, err)
		}
	}()
	if s == nil || s.pool == nil || s.ids == nil {
		return nil, ports.ErrUnavailable
	}
	if in.WantedID == uuid.Nil || in.Window < 0 || (in.Window > 0 && in.Window < time.Microsecond) || in.Window > 2*time.Minute {
		return nil, ports.ErrRejected
	}
	if in.Window == 0 {
		in.Window = 30 * time.Second
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	w, e := q.GetWantedItemByID(ctx, in.WantedID)
	if e != nil {
		return nil, e
	}
	hint, e := q.GetSubscriptionByID(ctx, w.SubscriptionID)
	if e != nil {
		return nil, e
	}
	if _, e = automaticTVLockOwner(ctx, q, hint.UserID); e != nil {
		return nil, e
	}
	if _, e = q.LockSearchPolicy(ctx, hint.QualityPolicyID); e != nil {
		return nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, hint.ID)
	if e != nil {
		return nil, e
	}
	if sub.UserID != hint.UserID || sub.QualityPolicyID != hint.QualityPolicyID {
		return nil, ports.ErrAcquisitionRoundAborted
	}
	target, e := q.GetMediaByID(ctx, sub.MediaID)
	if e != nil {
		return nil, e
	}
	if target.MediaType != "tv" {
		return nil, ports.ErrAutomaticTVNotApplicable
	}
	var oldRaw []byte
	e = tx.QueryRow(ctx, `SELECT to_jsonb(r) FROM automatic_tv_round r WHERE wanted_id=$1 AND state='running' FOR UPDATE`, in.WantedID).Scan(&oldRaw)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	var old *automaticTVRoundRow
	if e == nil {
		old, e = automaticTVDecodeRound(oldRaw, nil)
		if e != nil {
			return nil, e
		}
	}
	clock, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	observed := clock.Time.UTC()
	if old != nil {
		observed = old.CreatedAt
	}
	snap, sources, e := automaticTVSnapshot(ctx, q, sub, w.ID, observed)
	if e != nil {
		return nil, e
	}
	raw, hash, e := automaticTVSnapshotBytes(snap)
	if e != nil {
		return nil, e
	}
	if old != nil {
		if old.SnapshotHash != hash || !clock.Time.Before(old.Deadline) {
			return nil, ports.ErrAcquisitionRoundAborted
		}
		return &ports.AutomaticTVRound{ID: old.ID, State: old.State, Deadline: old.Deadline, Snapshot: snap, SnapshotHash: hash}, tx.Commit(ctx)
	}
	id, finalJob := s.ids.NewUUID(), s.ids.NewUUID()
	deadline := observed.Add(in.Window)
	if _, e = tx.Exec(ctx, `INSERT INTO job(id,kind,dedupe_key,priority,state,payload,max_attempts,not_before) VALUES($1,'automatic_tv_finalize',$2,6,'queued',jsonb_build_object('round_id',$3::uuid),8,$4)`, finalJob, id.String(), id, deadline); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO automatic_tv_round(id,wanted_id,subscription_id,owner_id,snapshot,snapshot_hash,finalize_job_id,created_at,deadline) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, w.ID, sub.ID, sub.UserID, raw, hash, finalJob, observed, deadline); e != nil {
		return nil, e
	}
	keys := make([]uuid.UUID, 0, len(sources))
	for key := range sources {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	for _, indexer := range keys {
		job := s.ids.NewUUID()
		_, params, paramsHash, e := canonicalAutomaticTVQuery(sources[indexer])
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO job(id,kind,dedupe_key,priority,state,payload,max_attempts,not_before) VALUES($1,'automatic_tv_source',$2,5,'queued',jsonb_build_object('round_id',$3::uuid,'indexer_id',$4::uuid),4,$5)`, job, id.String()+":"+indexer.String(), id, indexer, observed); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO automatic_tv_source(round_id,indexer_id,config_version,job_id,parameters,parameters_hash) VALUES($1,$2,$3,$4,$5,$6)`, id, indexer, snap.IndexerVersions[indexer.String()], job, params, paramsHash); e != nil {
			return nil, e
		}
	}
	safe, _ := json.Marshal(map[string]any{"round_id": id, "wanted_id": w.ID})
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "system", ActorID: sub.UserID.String(), Action: "automatic_tv.started", TargetType: "automatic_tv_round", TargetID: id.String(), Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "automatic_tv_round", AggregateID: id.String()}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "automatic_tv_round", AggregateID: id.String()})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "automatic_tv_round", AggregateID: id.String(), Seq: int64(seq), EventType: "automatic_tv.started", Payload: safe}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &ports.AutomaticTVRound{ID: id, State: "running", Deadline: deadline, Snapshot: snap, SnapshotHash: hash}, nil
}

// No caller-provided snapshot, clock or lease deadline confers authority.
func automaticTVAuthorize(ctx context.Context, q *db.Queries, t ports.AutomaticTVSourceTask) (*automaticTVRoundRow, *automaticTVSourceRow, error) {
	if q == nil || t.RoundID == uuid.Nil || t.IndexerID == uuid.Nil || t.Lease.ID == uuid.Nil || t.Lease.Owner == "" || t.Lease.Token < 1 {
		return nil, nil, ports.ErrAcquisitionRoundAborted
	}
	hint, e := automaticTVDecodeRound(q.AutomaticTVReadRound(ctx, t.RoundID))
	if e != nil {
		return nil, nil, e
	}
	if _, e = automaticTVLockOwner(ctx, q, hint.OwnerID); e != nil {
		return nil, nil, e
	}
	// Job before business and endpoint locks, shared by every permit callback.
	if _, e = q.LockJobForWrite(ctx, t.Lease.ID); e != nil {
		return nil, nil, e
	}
	if _, e = q.LockSearchPolicy(ctx, hint.Snapshot.PolicyID); e != nil {
		return nil, nil, e
	}
	sub, e := q.LockSearchSubscription(ctx, hint.SubscriptionID)
	if e != nil {
		return nil, nil, e
	}
	r, e := automaticTVDecodeRound(q.AutomaticTVLockRound(ctx, t.RoundID))
	if e != nil {
		return nil, nil, e
	}
	if r.State != "running" || sub.UserID != r.OwnerID || sub.QualityPolicyID != r.Snapshot.PolicyID {
		return nil, nil, ports.ErrAcquisitionRoundAborted
	}
	snap, sources, e := automaticTVSnapshot(ctx, q, sub, r.WantedID, r.CreatedAt)
	if e != nil {
		return nil, nil, e
	}
	_, hash, e := automaticTVSnapshotBytes(snap)
	if e != nil || hash != r.SnapshotHash {
		return nil, nil, ports.ErrAcquisitionRoundAborted
	}
	src, e := automaticTVDecodeSource(q.AutomaticTVLockSource(ctx, db.AutomaticTVLockSourceParams{RoundID: r.ID, IndexerID: t.IndexerID}))
	if e != nil {
		return nil, nil, e
	}
	if src.JobID != t.Lease.ID || (src.State != "queued" && src.State != "running") || src.ConfigVersion != snap.IndexerVersions[t.IndexerID.String()] {
		return nil, nil, ports.ErrAcquisitionRoundAborted
	}
	_, _, expected, e := canonicalAutomaticTVQuery(sources[t.IndexerID])
	if e != nil || expected != src.ParametersHash {
		return nil, nil, ports.ErrAcquisitionRoundAborted
	}
	valid, e := q.AutomaticTVJobValid(ctx, db.AutomaticTVJobValidParams{JobID: src.JobID, Owner: t.Lease.Owner, Token: t.Lease.Token, RoundID: r.ID, IndexerID: t.IndexerID, Deadline: pgtype.Timestamptz{Time: r.Deadline, Valid: true}})
	if e != nil {
		return nil, nil, e
	}
	if !valid {
		return nil, nil, ports.ErrAcquisitionRoundAborted
	}
	return r, src, nil
}
func automaticTVPages(ctx context.Context, q *db.Queries, t ports.AutomaticTVSourceTask) ([]automaticTVPage, error) {
	raw, e := q.AutomaticTVReadPages(ctx, db.AutomaticTVReadPagesParams{RoundID: t.RoundID, IndexerID: t.IndexerID})
	if e != nil {
		return nil, e
	}
	var pages []automaticTVPage
	if json.Unmarshal(raw, &pages) != nil {
		return nil, ports.ErrUnavailable
	}
	return pages, nil
}
func (s *AutomaticTVRoundStore) LoadAutomaticTVSource(ctx context.Context, t ports.AutomaticTVSourceTask) (out *ports.AutomaticTVSourceWork, err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	r, src, e := automaticTVAuthorize(ctx, q, t)
	if e != nil {
		return nil, e
	}
	pages, e := automaticTVPages(ctx, q, t)
	if e != nil {
		return nil, e
	}
	saved := make([]uuid.UUID, 0, len(pages))
	for i, p := range pages {
		if p.Page != int32(i) || p.SourceJobID != src.JobID || p.SourceToken != t.Lease.Token {
			return nil, ports.ErrAcquisitionRoundAborted
		}
		query, e := q.GetQueryExecution(ctx, p.QueryID)
		if e != nil {
			return nil, e
		}
		if query.State != "succeeded" {
			return nil, ports.ErrAcquisitionRoundAborted
		}
		saved = append(saved, p.QueryID)
	}
	// Loading is observation-only. Begin changes queued to running atomically with query creation.
	out = &ports.AutomaticTVSourceWork{Task: t, SnapshotHash: r.SnapshotHash, ConfigVersion: src.ConfigVersion, Deadline: r.Deadline, Query: src.Parameters, SavedQueries: saved, NextPage: int32(len(pages))}
	if _, _, e = automaticTVAuthorize(ctx, q, t); e != nil {
		return nil, e
	}
	return out, tx.Commit(ctx)
}
func (s *AutomaticTVRoundStore) FinishAutomaticTVSource(ctx context.Context, w ports.AutomaticTVSourceWork, outcome ports.AutomaticTVSourceOutcome) (err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	switch outcome {
	case ports.AutomaticTVSourceSucceeded, ports.AutomaticTVSourcePartial, ports.AutomaticTVSourceFailed, ports.AutomaticTVSourceDeferred, ports.AutomaticTVSourceTimeout:
	default:
		return ports.ErrRejected
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	r, src, e := automaticTVAuthorize(ctx, q, w.Task)
	if e != nil {
		return e
	}
	if w.SnapshotHash != r.SnapshotHash || w.ConfigVersion != src.ConfigVersion || !w.Deadline.Equal(r.Deadline) {
		return ports.ErrAcquisitionRoundAborted
	}
	job, e := q.GetAuthorizedSearchJob(ctx, db.GetAuthorizedSearchJobParams{ID: w.Task.Lease.ID, Kind: "automatic_tv_source", LeaseOwner: pgtype.Text{String: w.Task.Lease.Owner, Valid: true}, LeaseToken: w.Task.Lease.Token})
	if e != nil {
		return e
	}
	pages, e := automaticTVPages(ctx, q, w.Task)
	if e != nil {
		return e
	}
	success := 0
	for _, p := range pages {
		query, e := q.GetQueryExecution(ctx, p.QueryID)
		if e != nil {
			return e
		}
		if query.State == "running" {
			return ports.ErrAcquisitionRoundAborted
		}
		if query.State == "succeeded" {
			success++
		}
	}
	if outcome == ports.AutomaticTVSourceSucceeded && (success == 0 || success != len(pages)) {
		return ports.ErrRejected
	}
	if outcome == ports.AutomaticTVSourceDeferred && len(pages) > 0 {
		return ports.ErrRejected
	}
	if success > 0 && outcome != ports.AutomaticTVSourceSucceeded {
		outcome = ports.AutomaticTVSourcePartial
	}
	if _, e = tx.Exec(ctx, `UPDATE automatic_tv_source SET state=$3 WHERE round_id=$1 AND indexer_id=$2`, r.ID, src.IndexerID, string(outcome)); e != nil {
		return e
	}
	// Tail check happens after all source writes so trigger delays cannot admit an expired lease.
	tag, e := tx.Exec(ctx, `UPDATE job SET state=CASE WHEN $4 IN ('succeeded','partial') THEN 'succeeded' ELSE 'failed' END,result=jsonb_build_object('outcome',$4::text),finished_at=clock_timestamp(),updated_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL WHERE id=$1 AND kind='automatic_tv_source' AND state='running' AND lease_owner=$2 AND lease_token=$3 AND lease_until>clock_timestamp() AND clock_timestamp()<$5`, w.Task.Lease.ID, w.Task.Lease.Owner, w.Task.Lease.Token, string(outcome), r.Deadline)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ports.ErrAcquisitionRoundAborted
	}
	clock, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return e
	}
	if !clock.Time.Before(job.LeaseUntil.Time) || !clock.Time.Before(r.Deadline) {
		return ports.ErrAcquisitionRoundAborted
	}
	return tx.Commit(ctx)
}

func (s *AutomaticTVRoundStore) RecoverAutomaticTVSources(ctx context.Context, limit int32) (n int, err error) {
	defer func() { err = automaticTVError(ctx, err) }()
	if limit < 1 || limit > 100 {
		return 0, ports.ErrRejected
	}
	rows, e := s.pool.Query(ctx, `SELECT s.round_id,s.indexer_id,s.job_id FROM automatic_tv_source s JOIN job j ON j.id=s.job_id WHERE s.state IN ('queued','running') AND (j.state='running' AND j.lease_until<=clock_timestamp() OR j.state='queued') ORDER BY s.round_id,s.indexer_id LIMIT $1`, limit)
	if e != nil {
		return 0, e
	}
	var tasks []ports.AutomaticTVSourceTask
	for rows.Next() {
		var t ports.AutomaticTVSourceTask
		if e = rows.Scan(&t.RoundID, &t.IndexerID, &t.Lease.ID); e != nil {
			rows.Close()
			return 0, e
		}
		tasks = append(tasks, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, e
	}
	for _, t := range tasks {
		changed, e := s.recoverAutomaticTVSource(ctx, t)
		if e != nil {
			return n, e
		}
		if changed {
			n++
		}
	}
	return n, nil
}
func (s *AutomaticTVRoundStore) recoverAutomaticTVSource(ctx context.Context, t ports.AutomaticTVSourceTask) (bool, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	r, e := automaticTVDecodeRound(q.AutomaticTVReadRound(ctx, t.RoundID))
	if e != nil {
		return false, e
	}
	account, e := q.LockAcquisitionCommandUser(ctx, r.OwnerID)
	if e != nil {
		return false, e
	}
	current := account.IsActive && (account.Role == ports.RoleAdmin || account.Role == ports.RoleMember)
	if _, e = q.LockJobForWrite(ctx, t.Lease.ID); e != nil {
		return false, e
	}
	if _, e = q.LockSearchPolicy(ctx, r.Snapshot.PolicyID); e != nil {
		return false, e
	}
	sub, e := q.LockSearchSubscription(ctx, r.SubscriptionID)
	if e != nil {
		return false, e
	}
	r, e = automaticTVDecodeRound(q.AutomaticTVLockRound(ctx, r.ID))
	if e != nil {
		return false, e
	}
	current = current && sub.UserID == r.OwnerID && sub.QualityPolicyID == r.Snapshot.PolicyID
	if current {
		snapshot, _, checkErr := automaticTVSnapshot(ctx, q, sub, r.WantedID, r.CreatedAt)
		if checkErr != nil && !errors.Is(checkErr, ports.ErrAcquisitionRoundAborted) {
			return false, checkErr
		}
		_, hash, hashErr := automaticTVSnapshotBytes(snapshot)
		current = checkErr == nil && hashErr == nil && hash == r.SnapshotHash
	}
	src, e := automaticTVDecodeSource(q.AutomaticTVLockSource(ctx, db.AutomaticTVLockSourceParams{RoundID: r.ID, IndexerID: t.IndexerID}))
	if e != nil {
		return false, e
	}
	if src.JobID != t.Lease.ID || (src.State != "queued" && src.State != "running") {
		return false, nil
	}
	var state, owner string
	var token int64
	var expired, before, original, retryable bool
	e = tx.QueryRow(ctx, `SELECT state,COALESCE(lease_owner,''),lease_token,COALESCE(lease_until<=clock_timestamp(),true),clock_timestamp()<$2,kind='automatic_tv_source' AND dedupe_key=$3::uuid::text||':'||$4::uuid::text AND payload=jsonb_build_object('round_id',$3::uuid,'indexer_id',$4::uuid),attempts<max_attempts FROM job WHERE id=$1`, src.JobID, r.Deadline, r.ID, src.IndexerID).Scan(&state, &owner, &token, &expired, &before, &original, &retryable)
	if e != nil {
		return false, e
	}
	if !original {
		return false, ports.ErrAcquisitionRoundAborted
	}
	if state != "queued" && (state != "running" || !expired) {
		return false, nil
	}
	pages, e := automaticTVPages(ctx, q, t)
	if e != nil {
		return false, e
	}
	if len(pages) == 0 && before && current && retryable && r.State == "running" {
		if state != "running" {
			return false, nil
		}
		tag, e := tx.Exec(ctx, `UPDATE job SET state='queued',lease_token=lease_token+1,lease_owner=NULL,lease_until=NULL,not_before=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND state='running' AND lease_token=$2 AND COALESCE(lease_owner,'')=$3 AND lease_until<=clock_timestamp()`, src.JobID, token, owner)
		if e != nil {
			return false, e
		}
		if tag.RowsAffected() != 1 {
			return false, nil
		}
		return true, tx.Commit(ctx)
	}
	// A begun query is never replayed. Leave its physical permit and published pages intact.
	outcome := "timeout"
	for _, p := range pages {
		query, e := q.GetQueryExecution(ctx, p.QueryID)
		if e != nil {
			return false, e
		}
		if query.State == "succeeded" {
			outcome = "partial"
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE automatic_tv_source SET state=$3 WHERE round_id=$1 AND indexer_id=$2`, r.ID, src.IndexerID, outcome); e != nil {
		return false, e
	}
	tag, e := tx.Exec(ctx, `UPDATE job SET state='failed',result=jsonb_build_object('outcome',$4::text),lease_token=lease_token+1,lease_owner=NULL,lease_until=NULL,finished_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND lease_token=$2 AND COALESCE(lease_owner,'')=$3 AND (state='queued' OR state='running' AND lease_until<=clock_timestamp())`, src.JobID, token, owner, outcome)
	if e != nil {
		return false, e
	}
	if tag.RowsAffected() != 1 {
		return false, ports.ErrAcquisitionRoundAborted
	}
	return true, tx.Commit(ctx)
}
