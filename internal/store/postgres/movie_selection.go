package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"strconv"
)

var _ ports.MovieSelectionRepository = (*AcquisitionStore)(nil)

func selectedMovieActor(a *ports.Actor) error {
	return acquisitionCommandActor(a)
}
func readMovieSelection(ctx context.Context, q *db.Queries, a *ports.Actor, c ports.MovieSelection) (out ports.MovieSelectionSnapshot, err error) {
	if e := selectedMovieActor(a); e != nil {
		return out, e
	}
	root, e := q.GetLibraryRootByID(ctx, c.RootID)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return out, ports.ErrNotFound
		}
		return out, ports.ErrUnavailable
	}
	candidate, e := q.GetCandidateByID(ctx, c.CandidateID)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return out, ports.ErrNotFound
		}
		return out, ports.ErrUnavailable
	}
	idx, e := q.GetIndexerByID(ctx, candidate.IndexerID)
	if e != nil {
		return out, ports.ErrUnavailable
	}
	client, e := q.ReadSelectedMovieDownloaderSnapshot(ctx, c.DownloaderID)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return out, ports.ErrNotFound
		}
		return out, ports.ErrUnavailable
	}
	policy, e := q.GetPolicyByID(ctx, c.PolicyID)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return out, ports.ErrNotFound
		}
		return out, ports.ErrUnavailable
	}
	revision, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: c.PolicyID, Revision: policy.CurrentRevision})
	if e != nil {
		return out, ports.ErrUnavailable
	}
	var rules quality.PolicyRules
	if json.Unmarshal(revision.Rules, &rules) != nil {
		return out, ports.ErrUnavailable
	}
	observed, e := q.GetSearchDatabaseTime(ctx)
	if e != nil {
		return out, ports.ErrUnavailable
	}
	evidence, e := q.HasSelectedMovieCandidateObservation(ctx, db.HasSelectedMovieCandidateObservationParams{IndexerID: idx.ID, ConfigVersion: idx.ConfigVersion, CandidateID: candidate.ID})
	if e != nil {
		return out, ports.ErrUnavailable
	}
	var identity any
	existing, e := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "tmdb.movie", ExternalID: strconv.FormatInt(c.TMDBID, 10)})
	if e == nil {
		unit, x := q.GetMediaUnit(ctx, db.GetMediaUnitParams{MediaID: existing.ID, UnitKey: media.FormatMovieUnitKey()})
		if x != nil && !errors.Is(x, pgx.ErrNoRows) {
			return out, ports.ErrUnavailable
		}
		identity = []any{existing, unit}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return out, ports.ErrUnavailable
	}
	var profile quality.Profile
	if json.Unmarshal(candidate.QualityProfile, &profile) != nil {
		profile = quality.Profile{}
	}
	out.Rule = media.CandidateRuleInput{CandidateID: candidate.ID.String(), PolicyID: c.PolicyID, PolicyRevision: policy.CurrentRevision, Title: candidate.Title, RawProfile: string(candidate.QualityProfile), Profile: profile, Rules: rules, SizeBytes: candidate.SizeBytes, HasReference: len(candidate.LinkEncrypted) > 0, SourceVersion: candidate.SourceConfigVersion}
	out.Current = true
	switch {
	case root.MediaType != "movie":
		out.Current = false
		out.Reason = "root_type_mismatch"
	case root.Health != "online" || !root.BaselineCompleted:
		out.Current = false
		out.Reason = "root_unready"
	case candidate.SourceConfigVersion < 1 || idx.ConfigVersion != candidate.SourceConfigVersion || idx.Health != "online" || !evidence:
		out.Current = false
		out.Reason = "source_unproven"
	}
	material := quality.AcquisitionSourceStamp(candidate.Title, candidate.InfoHash.String, candidate.LinkEncrypted, candidate.QualityProfile)
	raw, _ := json.Marshal([]any{"movie.selection.snapshot.v1", c, root, idx.ID, idx.ConfigVersion, idx.Health, material, candidate.SizeBytes, client.ConfigDigest, policy.ID, policy.CurrentRevision, revision.Rules, identity, out.Current, out.Reason})
	sum := sha256.Sum256(raw)
	out.Stamp = hex.EncodeToString(sum[:])
	out.ObservedAt = observed.Time
	return out, nil
}
func (s *AcquisitionStore) ReadMovieSelection(ctx context.Context, a *ports.Actor, c ports.MovieSelection) (ports.MovieSelectionSnapshot, error) {
	if e := selectedMovieActor(a); e != nil {
		return ports.MovieSelectionSnapshot{}, e
	}
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return ports.MovieSelectionSnapshot{}, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		if errors.Is(e, ports.ErrForbidden) {
			return ports.MovieSelectionSnapshot{}, e
		}
		return ports.MovieSelectionSnapshot{}, ports.ErrUnavailable
	}
	out, e := readMovieSelection(ctx, q, a, c)
	if e != nil {
		return out, e
	}
	if tx.Commit(ctx) != nil {
		return ports.MovieSelectionSnapshot{}, ports.ErrUnavailable
	}
	return out, nil
}
func readMovieSelectionReceipt(ctx context.Context, q *db.Queries, a *ports.Actor, c ports.MovieSelection, key, hash string) (*ports.MovieSelectionReceipt, error) {
	if e := selectedMovieActor(a); e != nil {
		return nil, e
	}
	if _, e := q.GetLibraryRootByID(ctx, c.RootID); e != nil {
		return nil, ports.ErrNotFound
	}
	old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	var r ports.MovieSelectionReceipt
	if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &r) != nil || r.RootID != c.RootID || r.CandidateID == uuid.Nil || r.OperationID == uuid.Nil {
		return nil, ports.ErrMovieSelectionConflict
	}
	return &r, nil
}
func (s *AcquisitionStore) ReadMovieSelectionReceipt(ctx context.Context, a *ports.Actor, c ports.MovieSelection, key, hash string) (*ports.MovieSelectionReceipt, error) {
	if e := selectedMovieActor(a); e != nil {
		return nil, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		if errors.Is(e, ports.ErrForbidden) {
			return nil, e
		}
		return nil, ports.ErrUnavailable
	}
	r, e := readMovieSelectionReceipt(ctx, q, a, c, key, hash)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return r, nil
}
func selectedMovieStamp(snapshot ports.MovieSelectionSnapshot, c ports.MovieSelection, m ports.SelectedMovieMetadata) string {
	raw, _ := json.Marshal([]any{"movie.selection.v1", snapshot.Stamp, c, m, media.EpisodeParserVersion, media.CandidateDecisionVersion})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *AcquisitionStore) CommitMovieSelection(ctx context.Context, a *ports.Actor, c ports.MovieSelection, m ports.SelectedMovieMetadata, stamp, key, hash string) (out *ports.MovieSelectionReceipt, err error) {
	if e := selectedMovieActor(a); e != nil {
		return nil, e
	}
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrMovieSelectionConflict) && !errors.Is(err, ports.ErrAcquisitionUnsupportedCoverage) && !errors.Is(err, ports.ErrAcquisitionAlreadyOwned) && !errors.Is(err, ports.ErrAcquisitionOwnershipUncertain) && !errors.Is(err, ports.ErrAcquisitionLocalReuseRequired) && !errors.Is(err, ports.ErrAcquisitionUnitAlreadyReserved) && !errors.Is(err, ports.ErrForbidden) && !errors.Is(err, ports.ErrNotFound) {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	if e := selectedMovieActor(a); e != nil {
		return nil, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "movie.selection.command", AggregateID: string(lock)}); e != nil {
		return nil, e
	}
	if cached, e := readMovieSelectionReceipt(ctx, q, a, c, key, hash); e != nil || cached != nil {
		return cached, e
	}
	policy, e := q.LockSearchPolicy(ctx, c.PolicyID)
	if e != nil {
		return nil, e
	}
	root, e := q.LockAcquisitionRoot(ctx, c.RootID)
	if e != nil {
		return nil, e
	}
	if e = q.LockSelectedMovieIdentity(ctx, strconv.FormatInt(c.TMDBID, 10)); e != nil {
		return nil, e
	}
	existing, e := q.GetMediaByIdentity(ctx, db.GetMediaByIdentityParams{Namespace: "tmdb.movie", ExternalID: strconv.FormatInt(c.TMDBID, 10)})
	var existingUnit uuid.UUID
	if e == nil {
		u, x := q.GetMediaUnit(ctx, db.GetMediaUnitParams{MediaID: existing.ID, UnitKey: media.FormatMovieUnitKey()})
		if x == nil {
			existingUnit = u.ID
			if _, x = q.LockAcquisitionUnit(ctx, u.ID); x != nil {
				return nil, x
			}
		} else if !errors.Is(x, pgx.ErrNoRows) {
			return nil, x
		}
		if _, x = q.LockSearchMedia(ctx, existing.ID); x != nil {
			return nil, x
		}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	candidate, e := q.LockSubmissionCandidate(ctx, c.CandidateID)
	if e != nil {
		return nil, e
	}
	if _, e = q.LockSubmissionIndexer(ctx, candidate.IndexerID); e != nil {
		return nil, e
	}
	if e = q.LockSelectedMovieDownloader(ctx, c.DownloaderID); e != nil {
		return nil, e
	}
	snapshot, e := readMovieSelection(ctx, q, a, c)
	if e != nil {
		return nil, e
	}
	if !snapshot.Current || selectedMovieStamp(snapshot, c, m) != stamp || m.TMDBID != c.TMDBID {
		return nil, ports.ErrMovieSelectionConflict
	}
	rule := snapshot.Rule
	rule.TargetTitle = m.Title
	rule.Year = m.Year
	rule.Identities = map[string]string{"tmdb.movie": strconv.FormatInt(c.TMDBID, 10)}
	if m.IMDbID != "" {
		rule.Identities["imdb"] = m.IMDbID
	}
	decision := media.ExplainMovieCandidate(rule)
	if decision.Decision != "accept" && m.OriginalTitle != "" {
		rule.TargetTitle = m.OriginalTitle
		decision = media.ExplainMovieCandidate(rule)
	}
	if decision.Decision != "accept" {
		return nil, ports.ErrAcquisitionUnsupportedCoverage
	}
	mediaID := existing.ID
	if errors.Is(e, pgx.ErrNoRows) {
		mediaID = uuid.Nil
	}
	if mediaID == uuid.Nil {
		mediaID = s.idgen.NewUUID()
		if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: m.Title, OriginalTitle: pgtype.Text{String: m.OriginalTitle, Valid: m.OriginalTitle != ""}, Year: pgtype.Int4{Int32: int32(m.Year), Valid: true}, MediaType: "movie"}); e != nil {
			return nil, e
		}
		if _, e = q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: s.idgen.NewUUID(), MediaID: mediaID, Namespace: "tmdb.movie", ExternalID: strconv.FormatInt(c.TMDBID, 10)}); e != nil {
			return nil, e
		}
	}
	unitID := existingUnit
	if unitID == uuid.Nil {
		unitID = s.idgen.NewUUID()
		if _, e = q.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: media.FormatMovieUnitKey()}); e != nil {
			return nil, e
		}
	}
	r, e := s.reserveMovie(ctx, q, a, ports.AcquireMovieCommand{UnitIDs: []uuid.UUID{unitID}, RootID: c.RootID, CandidateID: c.CandidateID, DownloaderID: &c.DownloaderID, PolicyID: c.PolicyID, PolicyRevision: policy.CurrentRevision}, root, rule.Rules)
	if e != nil {
		return nil, e
	}
	out = &ports.MovieSelectionReceipt{OperationID: r.OperationID, MediaID: mediaID, UnitID: unitID, RootID: c.RootID, CandidateID: r.SourceCandidateID, Status: r.Status}
	raw, _ := json.Marshal(out)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "movie.selection.committed", TargetType: "acquisition", TargetID: r.OperationID.String(), Payload: raw}); e != nil {
		return nil, e
	}
	if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.idgen.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 202, ResponseBody: raw, ResourceID: pgtype.Text{String: r.OperationID.String(), Valid: true}}); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return out, nil
}
