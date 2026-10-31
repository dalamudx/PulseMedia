package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MovieDiscoveryStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewMovieDiscoveryStore(pool *pgxpool.Pool, ids ports.IDGen) *MovieDiscoveryStore {
	return &MovieDiscoveryStore{pool, ids}
}

var _ ports.MovieDiscoveryRepository = (*MovieDiscoveryStore)(nil)

type movieDiscoveryBasis struct {
	Actor        ports.Actor
	Subscription db.Subscription
	Policy       quality.Policy
	Query, Stamp string
	FileID       uuid.UUID
}

// Lock order matches the explicit upgrade boundary. Database facts, never the
// projection's cached score, decide whether the movie is still below cutoff.
func readMovieDiscoveryBasis(ctx context.Context, q *db.Queries, id uuid.UUID) (*movieDiscoveryBasis, error) {
	sub, e := q.GetSubscriptionByID(ctx, id)
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
	sub, e = q.LockUpgradeSubscription(ctx, id)
	if e != nil {
		return nil, e
	}
	if sub.UserID.String() != actor.ID || sub.QualityPolicyID != policy.ID {
		return nil, ports.ErrRejected
	}
	if sub.State != "active" || sub.QualityPolicyRevision != policy.CurrentRevision || !quality.UpgradeMode(sub.UpgradeMode).Enabled() {
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
	if root.Health != "online" || !root.BaselineCompleted || root.MediaType != "movie" {
		return nil, ports.ErrRejected
	}
	target, e := q.GetMediaByID(ctx, sub.MediaID)
	if e != nil {
		return nil, e
	}
	units, e := q.ListUpgradeUnits(ctx, target.ID)
	if e != nil {
		return nil, e
	}
	if target.MediaType != "movie" || len(units) != 1 || units[0].UnitKey != "movie" || units[0].Retired {
		return nil, ports.ErrRejected
	}
	unit := units[0]
	files, e := q.ListUpgradeFiles(ctx, db.ListUpgradeFilesParams{RootID: root.ID, UnitID: unit.ID})
	if e != nil {
		return nil, e
	}
	if len(files) != 1 || files[0].UnitCount != 1 {
		return nil, ports.ErrRejected
	}
	facts, e := q.ListUpgradeFacts(ctx, db.ListUpgradeFactsParams{SourceFileID: pgtype.UUID{Bytes: files[0].ID, Valid: true}, UnitID: unit.ID, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}})
	if e != nil {
		return nil, e
	}
	if len(facts) != 1 {
		return nil, ports.ErrRejected
	}
	wanted, e := listTVSelectionWanted(ctx, q, id)
	if e != nil {
		return nil, e
	}
	if len(wanted) != 1 || wanted[0].UnitID != unit.ID || !wanted[0].Current || !wanted[0].IsSatisfied || wanted[0].IsSkipped || wanted[0].Protected || wanted[0].FactStage != "imported" {
		return nil, ports.ErrRejected
	}
	var current quality.Profile
	if json.Unmarshal(facts[0].QualityProfile, &current) != nil || !quality.MovieDiscoveryAllowed(rules, current, quality.UpgradeMode(sub.UpgradeMode)) {
		return nil, ports.ErrRejected
	}
	now, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return nil, e
	}
	if facts[0].ExpiresAt.Valid && !facts[0].ExpiresAt.Time.After(now.Time) {
		return nil, ports.ErrRejected
	}
	query := strings.TrimSpace(target.Title)
	if target.Year.Valid {
		query = fmt.Sprintf("%s %d", query, target.Year.Int32)
	}
	if !utf8.ValidString(query) || strings.ContainsFunc(query, unicode.IsControl) || len([]rune(query)) < 1 || len([]rune(query)) > 256 {
		return nil, ports.ErrRejected
	}
	// Exclude projection write tokens and refresh times: a harmless re-evaluation
	// must not invalidate a search; all actual inputs are frozen separately.
	stamp := coverageDigest([]any{"movie.discovery.v1", actor, sub, policy, revision, root, target, unit, files, facts, wanted[0].ID, wanted[0].IsSkipped, wanted[0].IsSatisfied, wanted[0].FactStage})
	return &movieDiscoveryBasis{Actor: actor, Subscription: sub, Query: query, Stamp: stamp, FileID: files[0].ID, Policy: quality.Policy{ID: policy.ID, Revision: revision.Revision, ResolutionOrder: rules.ResolutionOrder, SourceOrder: rules.SourceOrder, PreferHDR: rules.PreferHDR, CustomWords: rules.CustomWords}}, nil
}

// AuthorizeMovieDiscoverySearch is a no-op for ordinary interactive searches.
// Call before subscription/header/source/job locks at every existing source fence.
func AuthorizeMovieDiscoverySearch(ctx context.Context, q *db.Queries, searchID uuid.UUID) error {
	installed, e := q.HasMovieDiscoverySchema(ctx)
	if e != nil {
		return e
	}
	if !installed {
		return nil
	}
	raw, e := q.ReadMovieDiscoverySearch(ctx, searchID)
	if e != nil {
		return e
	}
	var run struct {
		SubscriptionID uuid.UUID `json:"subscription_id"`
		InputStamp     string    `json:"input_stamp"`
	}
	if json.Unmarshal(raw, &run) != nil {
		return ports.ErrUnavailable
	}
	if run.SubscriptionID == uuid.Nil {
		return nil
	}
	basis, e := readMovieDiscoveryBasis(ctx, q, run.SubscriptionID)
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

func (s *MovieDiscoveryStore) ListDueMovieDiscoveries(ctx context.Context, limit int32) ([]uuid.UUID, error) {
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	if limit < 1 || limit > 20 {
		limit = 20
	}
	ids, e := db.New(s.pool).ListDueMovieDiscoveries(ctx, limit)
	return ids, interactiveError(ctx, e)
}

func (s *MovieDiscoveryStore) ScheduleMovieDiscovery(ctx context.Context, id uuid.UUID) (scheduled bool, err error) {
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
	// Share the original manual-search account mutex before checking its live cap.
	sub, e := q.GetSubscriptionByID(ctx, id)
	if e != nil {
		return false, e
	}
	creator := &ports.Actor{Type: ports.ActorTypeUser, ID: sub.UserID.String(), Role: ports.RoleAdmin}
	if e = interactiveActor(ctx, tx, creator, true); e != nil {
		return false, e
	}
	basis, basisErr := readMovieDiscoveryBasis(ctx, q, id)
	if basisErr != nil && !errors.Is(basisErr, ports.ErrRejected) && !errors.Is(basisErr, ports.ErrForbidden) {
		return false, basisErr
	}
	// Even ineligible subscriptions receive a bounded retry time so they cannot
	// starve later due subscriptions. Permission failures never create a search.
	if e = q.EnsureMovieDiscovery(ctx, id); e != nil {
		return false, e
	}
	due, e := q.LockMovieDiscoveryDue(ctx, id)
	if e != nil {
		return false, e
	}
	if !due {
		return false, tx.Commit(ctx)
	}
	if e = q.AdvanceMovieDiscovery(ctx, id); e != nil {
		return false, e
	}
	if basisErr != nil {
		return false, tx.Commit(ctx)
	}
	var bound []uuid.UUID
	if json.Unmarshal(basis.Subscription.IndexerIds, &bound) != nil || len(bound) == 0 || len(bound) > 64 {
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
	raw, e := q.CreateMovieDiscoverySearch(ctx, db.CreateMovieDiscoverySearchParams{ID: searchID, ActorID: basis.Subscription.UserID, ActorRole: basis.Actor.Role, Query: basis.Query, MediaID: basis.Subscription.MediaID, SubscriptionID: id, RankingPolicy: policy, SourceCount: int32(len(sources))})
	if e != nil {
		return false, e
	}
	header, e := interactiveDecode(raw, nil)
	if e != nil {
		return false, e
	}
	for _, src := range sources {
		jobID := s.ids.NewUUID()
		payload, _ := json.Marshal(ports.InteractiveSearchSourceTask{SearchID: searchID, IndexerID: src.ID, ConfigVersion: src.ConfigVersion})
		if e = q.CreateMovieDiscoveryJob(ctx, db.CreateMovieDiscoveryJobParams{ID: jobID, DedupeKey: searchID.String() + ":" + src.ID.String(), Payload: payload}); e != nil {
			return false, e
		}
		if e = q.CreateMovieDiscoverySource(ctx, db.CreateMovieDiscoverySourceParams{SearchID: searchID, IndexerID: src.ID, ConfigVersion: src.ConfigVersion, JobID: jobID}); e != nil {
			return false, e
		}
	}
	if e = q.CreateMovieDiscoveryRun(ctx, db.CreateMovieDiscoveryRunParams{SearchID: searchID, SubscriptionID: id, InputStamp: basis.Stamp}); e != nil {
		return false, e
	}
	if e = q.SetMovieDiscoverySearch(ctx, db.SetMovieDiscoverySearchParams{SubscriptionID: id, SearchID: pgtype.UUID{Bytes: searchID, Valid: true}}); e != nil {
		return false, e
	}
	if e = (&InteractiveSearchStore{pool: s.pool, ids: s.ids}).save(ctx, tx, &basis.Actor, header.public(), "create", "movie-discovery:"+searchID.String(), basis.Stamp); e != nil {
		return false, e
	}
	if e = AuthorizeMovieDiscoverySearch(ctx, q, searchID); e != nil {
		return false, e
	}
	if e = tx.Commit(ctx); e != nil {
		return false, e
	}
	return true, nil
}

func (s *MovieDiscoveryStore) ReadMovieDiscovery(ctx context.Context, a *ports.Actor, id uuid.UUID) (out *ports.MovieDiscoveryStatus, err error) {
	defer func() { err = interactiveError(ctx, err) }()
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
	// The authenticated caller may inspect only searches created for this same account.
	q := db.New(tx)
	sub, e := q.GetSubscriptionByID(ctx, id)
	if e != nil {
		return nil, e
	}
	if sub.UserID.String() != a.ID {
		return nil, ports.ErrForbidden
	}
	out = &ports.MovieDiscoveryStatus{SubscriptionID: id, State: "not_scheduled"}
	schedule, e := q.ReadMovieDiscoverySchedule(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, tx.Commit(ctx)
	}
	if e != nil {
		return nil, e
	}
	out.NextDiscoveryAt = &schedule.NextDiscoveryAt.Time
	out.State = "waiting"
	if schedule.SearchID.Valid {
		search := uuid.UUID(schedule.SearchID.Bytes)
		if e = AuthorizeMovieDiscoverySearch(ctx, q, search); e != nil {
			if errors.Is(e, ports.ErrRejected) || errors.Is(e, ports.ErrForbidden) || errors.Is(e, ports.ErrInteractiveSearchConflict) {
				out.State = "stale"
				return out, tx.Commit(ctx)
			}
			return nil, e
		}
		basis, e := readMovieDiscoveryBasis(ctx, q, id)
		if e != nil {
			return nil, e
		}
		out.CurrentFileID = &basis.FileID
		out.SearchID = &search
		out.State, e = q.ReadMovieDiscoveryState(ctx, search)
		if e != nil {
			return nil, e
		}
	}
	return out, tx.Commit(ctx)
}
