package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	sd "github.com/dalamudx/PulseMedia/internal/domain/subscription"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type TVEpisodeDiscoveryStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewTVEpisodeDiscoveryStore(pool *pgxpool.Pool, ids ports.IDGen) *TVEpisodeDiscoveryStore {
	return &TVEpisodeDiscoveryStore{pool, ids}
}

var _ ports.TVEpisodeDiscoveryRepository = (*TVEpisodeDiscoveryStore)(nil)

type tvEpisodeDiscoveryBasis struct {
	Actor        ports.Actor
	Subscription db.Subscription
	Policy       quality.Policy
	Query, Stamp string
	FileID       uuid.UUID
}

// Account, policy, subscription, root and canonical unit locks follow the explicit
// TV boundary. Cached projection scores never substitute for known imported facts.
func readTVEpisodeDiscoveryBasis(ctx context.Context, q *db.Queries, key ports.TVEpisodeDiscoveryUnit) (*tvEpisodeDiscoveryBasis, error) {
	sub, e := q.GetSubscriptionByID(ctx, key.SubscriptionID)
	if e != nil {
		return nil, e
	}
	account, e := q.InteractiveSearchLockAccount(ctx, sub.UserID)
	if e != nil {
		return nil, e
	}
	if !account.IsActive || account.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	actor := ports.Actor{Type: ports.ActorTypeUser, ID: sub.UserID.String(), Role: account.Role}
	policy, e := q.LockUpgradePolicy(ctx, sub.QualityPolicyID)
	if e != nil {
		return nil, e
	}
	sub, e = q.LockUpgradeSubscription(ctx, key.SubscriptionID)
	if e != nil {
		return nil, e
	}
	if sub.UserID.String() != actor.ID || sub.QualityPolicyID != policy.ID || sub.State != "active" || sub.QualityPolicyRevision != policy.CurrentRevision || !quality.UpgradeMode(sub.UpgradeMode).Enabled() {
		return nil, ports.ErrRejected
	}
	revision, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: policy.ID, Revision: policy.CurrentRevision})
	if e != nil {
		return nil, e
	}
	var rules quality.PolicyRules
	if json.Unmarshal(revision.Rules, &rules) != nil || quality.ValidateRules(rules) != nil {
		return nil, ports.ErrRejected
	}
	root, e := q.LockUpgradeRoot(ctx, sub.RootID)
	if e != nil {
		return nil, e
	}
	if root.Health != "online" || !root.BaselineCompleted || root.MediaType != "tv" {
		return nil, ports.ErrRejected
	}
	units, e := q.ListUpgradeUnits(ctx, sub.MediaID)
	if e != nil {
		return nil, e
	}
	var unit db.MediaUnit
	found := false
	for _, u := range units {
		if u.ID == key.UnitID {
			unit = u
			found = true
			break
		}
	}
	if !found {
		return nil, ports.ErrNotFound
	}
	work, e := q.LockSearchMedia(ctx, sub.MediaID)
	if e != nil {
		return nil, e
	}
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	if work.MediaType != "tv" || work.MetadataVersion < 1 || !work.Year.Valid || work.Year.Int32 < 1 || work.Year.Int32 > 9999 || unit.Retired || unit.MetadataVersion < 1 || !unit.AirDate.Valid || unit.AirDate.Time.After(now.Time) || !unit.SeasonNumber.Valid || !unit.EpisodeNumber.Valid || unit.AbsoluteNumber.Valid || unit.SeasonNumber.Int32 < 1 || unit.SeasonNumber.Int32 > 999 || unit.EpisodeNumber.Int32 < 1 || unit.EpisodeNumber.Int32 > 9999 || unit.UnitKey != media.FormatEpisodeUnitKey(int(unit.SeasonNumber.Int32), int(unit.EpisodeNumber.Int32)) {
		return nil, ports.ErrRejected
	}
	var scope sd.Scope
	scope.Type, scope.IncludeSpecials = sub.ScopeType, sub.IncludeSpecials
	if json.Unmarshal(sub.Seasons, &scope.Seasons) != nil || json.Unmarshal(sub.SelectedUnitIds, &scope.Selected) != nil {
		return nil, ports.ErrRejected
	}
	if !sd.ScopeContains(scope, sd.ScopeUnit{ID: unit.ID, Season: int(unit.SeasonNumber.Int32), Retired: unit.Retired, TV: true}) {
		return nil, ports.ErrRejected
	}
	identities, e := q.ListSearchMediaIdentities(ctx, work.ID)
	if e != nil {
		return nil, e
	}
	var tmdb int64
	count := 0
	for _, id := range identities {
		if id.Namespace == "tmdb.tv" {
			count++
			tmdb, e = strconv.ParseInt(id.ExternalID, 10, 64)
			if e != nil {
				return nil, ports.ErrRejected
			}
		}
	}
	if count != 1 || tmdb < 1 {
		return nil, ports.ErrRejected
	}
	episode, e := q.GetTMDBEpisodeIdentityForUnit(ctx, unit.ID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrRejected
	}
	if e != nil {
		return nil, e
	}
	if episode.ExternalID < 1 || episode.TmdbSeriesID != tmdb {
		return nil, ports.ErrRejected
	}
	var mapping any
	if sub.EpisodeMappingRevision.Valid {
		m, e := q.GetCurrentEpisodeMapping(ctx, sub.ID)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, ports.ErrRejected
		}
		if e != nil {
			return nil, e
		}
		if m.Revision < 1 || m.Mode != "standard" || m.MediaID != work.ID || m.MediaVersion != work.MetadataVersion {
			return nil, ports.ErrRejected
		}
		mapping = m
	}
	files, e := q.ListUpgradeFiles(ctx, db.ListUpgradeFilesParams{RootID: root.ID, UnitID: unit.ID})
	if e != nil {
		return nil, e
	}
	if len(files) != 1 || files[0].UnitCount != 1 || files[0].SizeBytes < 1 || !tvEpisodeUpgradeVideoPath(files[0].RelPath) {
		return nil, ports.ErrRejected
	}
	facts, e := q.ListUpgradeFacts(ctx, db.ListUpgradeFactsParams{SourceFileID: pgtype.UUID{Bytes: files[0].ID, Valid: true}, UnitID: unit.ID, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}})
	if e != nil {
		return nil, e
	}
	if len(facts) != 1 || (facts[0].ExpiresAt.Valid && !facts[0].ExpiresAt.Time.After(now.Time)) {
		return nil, ports.ErrRejected
	}
	var current quality.Profile
	if json.Unmarshal(facts[0].QualityProfile, &current) != nil || !quality.MovieDiscoveryAllowed(rules, current, quality.UpgradeMode(sub.UpgradeMode)) {
		return nil, ports.ErrRejected
	}
	wanted, e := listTVSelectionWanted(ctx, q, sub.ID)
	if e != nil {
		return nil, e
	}
	found = false
	for _, w := range wanted {
		if w.UnitID == unit.ID {
			found = true
			if !w.Current || !w.IsSatisfied || w.IsSkipped || w.Protected || w.FactStage != "imported" {
				return nil, ports.ErrRejected
			}
		}
	}
	if !found {
		return nil, ports.ErrRejected
	}
	now, e = q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	if facts[0].ExpiresAt.Valid && !facts[0].ExpiresAt.Time.After(now.Time) {
		return nil, ports.ErrRejected
	}
	query := fmt.Sprintf("%s %d S%02dE%02d", strings.TrimSpace(work.Title), work.Year.Int32, unit.SeasonNumber.Int32, unit.EpisodeNumber.Int32)
	if !utf8.ValidString(query) || strings.ContainsFunc(query, unicode.IsControl) || len([]rune(query)) > 256 {
		return nil, ports.ErrRejected
	}
	// Scan counters and observation timestamps do not change the physical root.
	root.ScanToken, root.MutationSeq = 0, 0
	root.UpdatedAt = pgtype.Timestamptz{}
	rootIdentity, e := q.ReadTVEpisodeDiscoveryRootIdentity(ctx, root.ID)
	if e != nil {
		return nil, e
	}
	var physical struct {
		Device    int64  `json:"device"`
		Inode     int64  `json:"inode"`
		Path      string `json:"path"`
		MediaType string `json:"media_type"`
		Marker    string `json:"marker"`
	}
	if json.Unmarshal(rootIdentity, &physical) != nil || physical.Inode < 1 || physical.Path != root.Path || physical.MediaType != root.MediaType || physical.Marker != root.MarkerFilename {
		return nil, ports.ErrRejected
	}
	unit.FactsVersion = 0
	unit.UpdatedAt = pgtype.Timestamptz{}
	stamp := coverageDigest([]any{"tv.episode.discovery.v1", actor, sub, policy, revision, root, completeTVJSONDigest(rootIdentity), work, unit, identities, episode, mapping, files, facts})
	return &tvEpisodeDiscoveryBasis{actor, sub, quality.Policy{ID: policy.ID, Revision: revision.Revision, ResolutionOrder: rules.ResolutionOrder, SourceOrder: rules.SourceOrder, PreferHDR: rules.PreferHDR, CustomWords: rules.CustomWords}, query, stamp, files[0].ID}, nil
}

// Ordinary interactive searches are unaffected. Every source fence and snapshot
// calls this before object/header/source/job locks, including late result writes.
func AuthorizeTVEpisodeDiscoverySearch(ctx context.Context, q *db.Queries, searchID uuid.UUID) error {
	installed, e := q.HasTVEpisodeDiscoverySchema(ctx)
	if e != nil || !installed {
		return e
	}
	raw, e := q.ReadTVEpisodeDiscoverySearch(ctx, searchID)
	if e != nil {
		return e
	}
	var run struct {
		SubscriptionID uuid.UUID `json:"subscription_id"`
		UnitID         uuid.UUID `json:"unit_id"`
		InputStamp     string    `json:"input_stamp"`
	}
	if json.Unmarshal(raw, &run) != nil {
		return ports.ErrUnavailable
	}
	if run.SubscriptionID == uuid.Nil {
		return nil
	}
	basis, e := readTVEpisodeDiscoveryBasis(ctx, q, ports.TVEpisodeDiscoveryUnit{SubscriptionID: run.SubscriptionID, UnitID: run.UnitID})
	if e != nil {
		return e
	}
	if basis.Stamp != run.InputStamp {
		return ports.ErrInteractiveSearchConflict
	}
	h, e := interactiveDecode(q.InteractiveSearchReadHeader(ctx, searchID))
	if e != nil {
		return e
	}
	if h.SubscriptionID == nil || *h.SubscriptionID != basis.Subscription.ID || h.MediaID == nil || *h.MediaID != basis.Subscription.MediaID || h.ActorID.String() != basis.Actor.ID || h.ActorRole != basis.Actor.Role || h.Query != basis.Query || coverageDigest(h.RankingPolicy) != coverageDigest(basis.Policy) || !h.Deadline.Equal(h.CreatedAt.Add(30*time.Second)) {
		return ports.ErrInteractiveSearchConflict
	}
	return nil
}

func (s *TVEpisodeDiscoveryStore) ListDueTVEpisodeDiscoveries(ctx context.Context, limit int32) ([]ports.TVEpisodeDiscoveryUnit, error) {
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	if limit < 1 || limit > 20 {
		limit = 20
	}
	rows, e := db.New(s.pool).ListDueTVEpisodeDiscoveries(ctx, limit)
	if e != nil {
		return nil, interactiveError(ctx, e)
	}
	out := make([]ports.TVEpisodeDiscoveryUnit, 0, len(rows))
	for _, r := range rows {
		out = append(out, ports.TVEpisodeDiscoveryUnit{SubscriptionID: r.SubscriptionID, UnitID: r.UnitID})
	}
	return out, nil
}
func (s *TVEpisodeDiscoveryStore) ScheduleTVEpisodeDiscovery(ctx context.Context, key ports.TVEpisodeDiscoveryUnit) (scheduled bool, err error) {
	defer func() { err = interactiveError(ctx, err) }()
	if s == nil || s.pool == nil || s.ids == nil {
		return false, ports.ErrUnavailable
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	sub, e := q.GetSubscriptionByID(ctx, key.SubscriptionID)
	if e != nil {
		return false, e
	}
	creator := &ports.Actor{Type: ports.ActorTypeUser, ID: sub.UserID.String(), Role: ports.RoleAdmin}
	if e = interactiveActor(ctx, tx, creator, true); e != nil {
		return false, e
	}
	basis, basisErr := readTVEpisodeDiscoveryBasis(ctx, q, key)
	if basisErr != nil && !errors.Is(basisErr, ports.ErrRejected) && !errors.Is(basisErr, ports.ErrForbidden) && !errors.Is(basisErr, ports.ErrNotFound) {
		return false, basisErr
	}
	if e = q.EnsureTVEpisodeDiscovery(ctx, db.EnsureTVEpisodeDiscoveryParams{SubscriptionID: key.SubscriptionID, UnitID: key.UnitID}); e != nil {
		return false, e
	}
	due, e := q.LockTVEpisodeDiscoveryDue(ctx, db.LockTVEpisodeDiscoveryDueParams{SubscriptionID: key.SubscriptionID, UnitID: key.UnitID})
	if e != nil {
		return false, e
	}
	if !due {
		return false, tx.Commit(ctx)
	}
	// Ineligible units also advance, preventing one old unit from starving the batch.
	if e = q.AdvanceTVEpisodeDiscovery(ctx, db.AdvanceTVEpisodeDiscoveryParams{SubscriptionID: key.SubscriptionID, UnitID: key.UnitID}); e != nil {
		return false, e
	}
	if basisErr != nil {
		return false, tx.Commit(ctx)
	}
	var bound []uuid.UUID
	if json.Unmarshal(basis.Subscription.IndexerIds, &bound) != nil || len(bound) < 1 || len(bound) > 64 {
		return false, tx.Commit(ctx)
	}
	sources, e := q.ListMovieDiscoverySources(ctx, bound)
	if e != nil {
		return false, e
	}
	if len(sources) == 0 {
		return false, tx.Commit(ctx)
	}
	live, e := q.CountLiveMovieDiscoverySearches(ctx, basis.Subscription.UserID)
	if e != nil {
		return false, e
	}
	if live >= 4 {
		return false, tx.Commit(ctx)
	}
	searchID := s.ids.NewUUID()
	policy, e := json.Marshal(basis.Policy)
	if e != nil {
		return false, e
	}
	raw, e := q.CreateMovieDiscoverySearch(ctx, db.CreateMovieDiscoverySearchParams{ID: searchID, ActorID: basis.Subscription.UserID, ActorRole: basis.Actor.Role, Query: basis.Query, MediaID: basis.Subscription.MediaID, SubscriptionID: key.SubscriptionID, RankingPolicy: policy, SourceCount: int32(len(sources))})
	if e != nil {
		return false, e
	}
	header, e := interactiveDecode(raw, nil)
	if e != nil {
		return false, e
	}
	for _, src := range sources {
		jobID := s.ids.NewUUID()
		payload, e := json.Marshal(ports.InteractiveSearchSourceTask{SearchID: searchID, IndexerID: src.ID, ConfigVersion: src.ConfigVersion})
		if e != nil {
			return false, e
		}
		if e = q.CreateMovieDiscoveryJob(ctx, db.CreateMovieDiscoveryJobParams{ID: jobID, DedupeKey: searchID.String() + ":" + src.ID.String(), Payload: payload}); e != nil {
			return false, e
		}
		if e = q.CreateMovieDiscoverySource(ctx, db.CreateMovieDiscoverySourceParams{SearchID: searchID, IndexerID: src.ID, ConfigVersion: src.ConfigVersion, JobID: jobID}); e != nil {
			return false, e
		}
	}
	if e = q.CreateTVEpisodeDiscoveryRun(ctx, db.CreateTVEpisodeDiscoveryRunParams{SearchID: searchID, SubscriptionID: key.SubscriptionID, UnitID: key.UnitID, InputStamp: basis.Stamp}); e != nil {
		return false, e
	}
	if e = q.SetTVEpisodeDiscoverySearch(ctx, db.SetTVEpisodeDiscoverySearchParams{SubscriptionID: key.SubscriptionID, UnitID: key.UnitID, SearchID: pgtype.UUID{Bytes: searchID, Valid: true}}); e != nil {
		return false, e
	}
	if e = (&InteractiveSearchStore{pool: s.pool, ids: s.ids}).save(ctx, tx, &basis.Actor, header.public(), "create", "tv-episode-discovery:"+searchID.String(), basis.Stamp); e != nil {
		return false, e
	}
	if e = AuthorizeTVEpisodeDiscoverySearch(ctx, q, searchID); e != nil {
		return false, e
	}
	if e = tx.Commit(ctx); e != nil {
		return false, e
	}
	return true, nil
}
func (s *TVEpisodeDiscoveryStore) ReadTVEpisodeDiscovery(ctx context.Context, a *ports.Actor, key ports.TVEpisodeDiscoveryUnit) (*ports.TVEpisodeDiscoveryStatus, error) {
	// Each attempt is a complete read snapshot. Concurrent row updates can invalidate
	// a Repeatable Read lock wait; retry only serialization failures, never commands.
	for attempt := 0; ; attempt++ {
		out, err := s.readTVEpisodeDiscoverySnapshot(ctx, a, key)
		var cause *pgconn.PgError
		if attempt < 2 && ctx.Err() == nil && errors.As(err, &cause) && cause.Code == "40001" {
			continue
		}
		return out, interactiveError(ctx, err)
	}
}

func (s *TVEpisodeDiscoveryStore) readTVEpisodeDiscoverySnapshot(ctx context.Context, a *ports.Actor, key ports.TVEpisodeDiscoveryUnit) (out *ports.TVEpisodeDiscoveryStatus, err error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	if e = interactiveActor(ctx, tx, a, false); e != nil {
		return nil, e
	}
	q := db.New(tx)
	sub, e := q.GetSubscriptionByID(ctx, key.SubscriptionID)
	if e != nil {
		return nil, e
	}
	if sub.UserID.String() != a.ID {
		return nil, ports.ErrForbidden
	}
	units, e := q.ListUpgradeUnits(ctx, sub.MediaID)
	if e != nil {
		return nil, e
	}
	found := false
	for _, u := range units {
		if u.ID == key.UnitID {
			found = true
		}
	}
	if !found {
		return nil, ports.ErrNotFound
	}
	out = &ports.TVEpisodeDiscoveryStatus{SubscriptionID: key.SubscriptionID, UnitID: key.UnitID, State: "not_scheduled"}
	schedule, e := q.ReadTVEpisodeDiscoverySchedule(ctx, db.ReadTVEpisodeDiscoveryScheduleParams{SubscriptionID: key.SubscriptionID, UnitID: key.UnitID})
	if errors.Is(e, pgx.ErrNoRows) {
		return out, tx.Commit(ctx)
	}
	if e != nil {
		return nil, e
	}
	out.NextDiscoveryAt = &schedule.NextDiscoveryAt.Time
	out.State = "waiting"
	if schedule.SearchID.Valid {
		searchID := uuid.UUID(schedule.SearchID.Bytes)
		if e = AuthorizeTVEpisodeDiscoverySearch(ctx, q, searchID); e != nil {
			if errors.Is(e, ports.ErrRejected) || errors.Is(e, ports.ErrForbidden) || errors.Is(e, ports.ErrNotFound) || errors.Is(e, ports.ErrInteractiveSearchConflict) {
				out.State = "stale"
				return out, tx.Commit(ctx)
			}
			return nil, e
		}
		basis, e := readTVEpisodeDiscoveryBasis(ctx, q, key)
		if e != nil {
			return nil, e
		}
		out.CurrentFileID = &basis.FileID
		out.SearchID = &searchID
		out.State, e = q.ReadMovieDiscoveryState(ctx, searchID)
		if e != nil {
			return nil, e
		}
	}
	return out, tx.Commit(ctx)
}
