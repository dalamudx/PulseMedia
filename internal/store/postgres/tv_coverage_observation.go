package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"sort"
	"strings"
	"time"
)

// TVCoverageCandidateReviewStore reuses the mapping boundary without widening single review.
type TVCoverageCandidateReviewStore struct{ mapping *EpisodeMappingStore }

func NewTVCoverageCandidateReviewStore(mapping *EpisodeMappingStore) *TVCoverageCandidateReviewStore {
	return &TVCoverageCandidateReviewStore{mapping}
}

var _ ports.TVCoverageCandidateReviewRepository = (*TVCoverageCandidateReviewStore)(nil)

func coverageDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func coverageAccess(ctx context.Context, q *db.Queries, a *ports.Actor, id uuid.UUID) error {
	if e := episodeReviewActor(a); e != nil {
		return e
	}
	if e := lockAcquisitionUser(ctx, q, a); e != nil {
		return e
	}
	sub, e := q.GetSubscriptionByID(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	if e != nil {
		return e
	}
	if !a.CanAccessUser(sub.UserID.String()) {
		return ports.ErrForbidden
	}
	return nil
}
func (s *TVCoverageCandidateReviewStore) ReadTVCoverageCandidate(ctx context.Context, a *ports.Actor, id, candidate uuid.UUID) (*ports.TVCoverageCandidateReviewObservation, error) {
	tx, e := s.mapping.refresh.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := db.New(tx)
	if e = coverageAccess(ctx, q, a, id); e != nil {
		return nil, e
	}
	out, e := readTVCoverageCandidate(ctx, q, a, id, candidate)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return out, nil
}
func readTVCoverageCandidate(ctx context.Context, q *db.Queries, a *ports.Actor, id, candidateID uuid.UUID) (*ports.TVCoverageCandidateReviewObservation, error) {
	v, e := readEpisodeCandidate(ctx, q, a, id, candidateID)
	if e != nil {
		return nil, e
	}
	source, e := readEpisodeSourceContext(ctx, q, a, id)
	if e != nil {
		return nil, e
	}
	sub, e := q.GetSubscriptionByID(ctx, id)
	if e != nil {
		return nil, e
	}
	root, e := q.GetLibraryRootByID(ctx, sub.RootID)
	if e != nil {
		return nil, e
	}
	candidate, e := q.GetCandidateByID(ctx, candidateID)
	if e != nil {
		return nil, e
	}
	indexer, e := q.GetIndexerByID(ctx, candidate.IndexerID)
	if e != nil {
		return nil, e
	}
	var downloader any
	if sub.DownloaderID.Valid {
		client, err := q.GetDownloadClientByID(ctx, uuid.UUID(sub.DownloaderID.Bytes))
		if err != nil {
			return nil, err
		}
		// Bind the selected configuration without persisting destinations or credentials.
		downloader = []any{client.ID, client.ClientType, client.UpdatedAt, coverageDigest([]any{client.Url, client.Username, coverageDigest(client.PasswordEncrypted), json.RawMessage(client.RemotePathMap)})}
	}
	provenance, e := q.GetTVCoverageSourceEvidence(ctx, db.GetTVCoverageSourceEvidenceParams{IndexerID: indexer.ID, ConfigVersion: indexer.ConfigVersion, CandidateID: candidateID})
	if e != nil {
		return nil, e
	}
	mapping := v.Mapping
	mapping.ObservedAt = time.Time{}
	canonical := append([]ports.EpisodeSourceUnit(nil), source.Units...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].ID.String() < canonical[j].ID.String() })
	target := v.Target
	target.ObservedAt = time.Time{}
	target.EndpointsReady = false
	target.EndpointRetryAt = nil
	// Files, ownership, reservation and round state are observation facts, never material authority.
	facts, _ := json.Marshal(struct {
		Units any `json:"units"`
	}{target.Units})
	selection := make([]any, 0, len(target.Units))
	selected := map[string]bool{}
	for _, u := range target.Units {
		selection = append(selection, []any{u.ID, u.UnitID, u.Key, u.MetadataVersion, u.Skipped, u.Retired, u.AirDate})
		selected[u.Key] = !u.Skipped && !u.Retired
	}
	material, _ := json.Marshal(map[string]any{
		"version": "tv-coverage-review.material.v1", "parser": media.EpisodeParserVersion, "identity_parser": media.TVIdentitySuggestionVersion,
		"subscription": sub, "media_id": source.MediaID, "tmdb_id": source.TMDBID, "media_version": source.MediaVersion,
		"parent": []any{v.ParentTitle, v.ParentOriginalTitle, v.ParentYear}, "mapping": mapping, "canonical": canonical, "complete_seasons": source.CompleteSeasons,
		"candidate": []any{candidate.ID, candidate.IndexerID, coverageDigest(candidate.Guid), candidate.Title, candidate.SizeBytes, candidate.InfoHash, candidate.QualityProfile, coverageDigest(candidate.LinkEncrypted), candidate.SourceConfigVersion},
		"source":    []any{indexer.ID, indexer.ConfigVersion, indexer.Health, v.Current, json.RawMessage(provenance)}, "policy": []any{sub.QualityPolicyID, target.PolicyRevision, target.Rules, target.PolicyAvailable},
		"root": []any{root.ID, root.Generation, root.HealthRevision, root.Health, root.BaselineCompleted}, "selection": selection,
		"downloader": downloader,
	})
	materialStamp := coverageDigest(json.RawMessage(material))
	out := &ports.TVCoverageCandidateReviewObservation{SubscriptionID: id, CandidateID: candidateID, Material: material, Facts: facts, MaterialStamp: materialStamp, InputStamp: coverageDigest([]any{"tv-coverage-review.observation.v1", materialStamp, json.RawMessage(facts)}), Rows: []media.NumberingRow{}, HoldReason: "complete_normal_season_required"}
	if !v.Current || !mapping.Current || string(provenance) == "[]" {
		out.HoldReason = "source_or_mapping_changed"
		return out, nil
	}
	if media.UnsafeCandidateTitle(v.Name) || strings.ContainsAny(v.Name, "/\\") || len(v.Name) > 512 || media.HasConflictingTVIdentity(v.Name, mapping.TMDBID) {
		out.HoldReason = "invalid_title_identity"
		return out, nil
	}
	decision, _ := media.SuggestTVIdentity(v.Name, v.ParentTitle, v.ParentOriginalTitle, v.ParentYear, mapping.TMDBID)
	if decision == "reject" {
		out.HoldReason = "identity_conflict"
		return out, nil
	}
	units := make([]media.NumberingUnit, 0, len(canonical))
	for _, u := range canonical {
		units = append(units, media.NumberingUnit{ID: u.ID, EpisodeID: u.EpisodeID, Version: u.Version, Key: u.Key, Season: u.Season, Episode: u.Episode, Retired: u.Retired})
	}
	parsed := media.RecognizeRelease(v.Name)
	for _, proof := range source.CompleteSeasons {
		p := make([]media.NumberingUnit, 0, len(proof.Units))
		for _, u := range proof.Units {
			p = append(p, media.NumberingUnit{ID: u.UnitID, EpisodeID: u.TMDBEpisodeID, Version: u.UnitVersion, Key: media.FormatEpisodeUnitKey(proof.Season, u.EpisodeNumber), Season: proof.Season, Episode: u.EpisodeNumber})
		}
		rows, reason := media.ReviewWholeStandardSeason(v.Name, parsed, mapping.Mode, proof.Season, p, units, selected)
		if len(rows) >= 2 {
			out.Rows = rows
			out.CanReview = true
			out.HoldReason = ""
			break
		}
		out.HoldReason = reason
	}
	return out, nil
}
