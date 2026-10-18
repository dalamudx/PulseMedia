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
	"time"
)

func (s *EpisodeMappingStore) ReadEpisodeCandidate(ctx context.Context, a *ports.Actor, id, candidateID uuid.UUID) (*ports.EpisodeCandidateObservation, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if candidateID == uuid.Nil {
		return nil, ports.ErrInvalidEpisodeMapping
	}
	tx, e := s.refresh.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	out, e := readEpisodeCandidate(ctx, db.New(tx), a, id, candidateID)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
func readEpisodeCandidate(ctx context.Context, q *db.Queries, a *ports.Actor, id, candidateID uuid.UUID) (*ports.EpisodeCandidateObservation, error) {
	state, e := readEpisodeMappingState(ctx, q, a, id)
	if e != nil {
		return nil, e
	}
	candidate, e := q.GetCandidateByID(ctx, candidateID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	idx, e := q.GetIndexerByID(ctx, candidate.IndexerID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	sub, e := q.GetSubscriptionByID(ctx, id)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	var bound []uuid.UUID
	if json.Unmarshal(sub.IndexerIds, &bound) != nil {
		return nil, ports.ErrUnavailable
	}
	allowed := false
	for _, v := range bound {
		allowed = allowed || v == idx.ID
	}
	recent, e := q.HasSelectedMovieCandidateObservation(ctx, db.HasSelectedMovieCandidateObservationParams{IndexerID: idx.ID, ConfigVersion: idx.ConfigVersion, CandidateID: candidate.ID})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out := &ports.EpisodeCandidateObservation{Mapping: *state, CandidateID: candidate.ID, IndexerID: idx.ID, Name: candidate.Title, SourceVersion: candidate.SourceConfigVersion, Current: true}
	if !allowed {
		out.Current = false
		out.HoldReason = "source_not_bound"
	} else if candidate.SourceConfigVersion < 1 || candidate.SourceConfigVersion != idx.ConfigVersion || idx.Health != "online" {
		out.Current = false
		out.HoldReason = "source_changed"
	} else if !recent {
		out.Current = false
		out.HoldReason = "source_observation_expired"
	}
	canonical, e := readEpisodeSourceContext(ctx, q, a, id)
	if e != nil {
		return nil, e
	}
	out.CanonicalUnits = canonical.Units
	target, e := readSubscriptionStatus(ctx, q, a, id, nil)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out.Target = target
	if json.Unmarshal(candidate.QualityProfile, &out.Quality) != nil {
		out.Quality = quality.Profile{}
	}
	parent, e := q.GetMediaByID(ctx, state.MediaID)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	out.ParentTitle = parent.Title
	out.ParentOriginalTitle = parent.OriginalTitle.String
	if parent.Year.Valid {
		out.ParentYear = int(parent.Year.Int32)
	}
	stampTarget := target
	stampTarget.ObservedAt = time.Time{}
	// Query budget readiness is automation advice, not reviewed manual download authority.
	// Its clock boundary must not change a review digest without any entity/version change.
	stampTarget.EndpointsReady = false
	stampTarget.EndpointRetryAt = nil
	cipher := sha256.Sum256(candidate.LinkEncrypted)
	raw, e := json.Marshal([]any{"episode-candidate.inputs.v2", media.EpisodeParserVersion, media.TVIdentitySuggestionVersion, out.ParentTitle, out.ParentOriginalTitle, out.ParentYear, state.MediaID, state.InputStamp, state.IntentVersion, state.MappingRevision, state.SourceHash, state.Rows, state.ValidUntil, canonical.Units, stampTarget, candidate.ID, idx.ID, idx.ConfigVersion, candidate.SourceConfigVersion, candidate.Title, candidate.SizeBytes, candidate.InfoHash, candidate.QualityProfile, hex.EncodeToString(cipher[:]), allowed})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	hash := sha256.Sum256(raw)
	out.InputStamp = hex.EncodeToString(hash[:])
	return out, nil
}
