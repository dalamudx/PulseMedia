package subscription

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"sort"
	"strings"
	"time"
)

type EpisodeMappingService struct {
	repo   ports.EpisodeMappingRepository
	source *EpisodeSourceService
}

func NewEpisodeMappingService(repo ports.EpisodeMappingRepository, source *EpisodeSourceService) *EpisodeMappingService {
	return &EpisodeMappingService{repo, source}
}
func validMappingDraft(d ports.EpisodeMappingDraft) bool {
	switch d.Mode {
	case "standard":
		return d.GroupID == "" && d.PartID == "" && len(d.Entries) == 0
	case "group", "absolute":
		return d.GroupID != "" && len(d.GroupID) <= 128 && len(d.PartID) <= 128 && len(d.Entries) == 0
	case "manual":
		return d.GroupID == "" && d.PartID == "" && len(d.Entries) > 0 && len(d.Entries) <= 5000
	}
	return false
}
func (s *EpisodeMappingService) Preview(ctx context.Context, a *ports.Actor, id uuid.UUID, d ports.EpisodeMappingDraft) (*ports.EpisodeMappingPreview, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() || a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if !validMappingDraft(d) {
		return nil, ports.ErrInvalidEpisodeMapping
	}
	input, e := s.repo.ReadEpisodeSourceContext(ctx, a, id)
	if e != nil {
		return nil, e
	}
	p := &ports.EpisodeMappingPreview{SubscriptionID: id, MediaID: input.MediaID, Mode: d.Mode, MappingRevision: input.MappingRevision, InputStamp: input.InputStamp, Rows: []ports.EpisodeGroupMappingRow{}}
	if d.Mode == "group" || d.Mode == "absolute" {
		g, e := s.source.Preview(ctx, a, id, d.GroupID, d.PartID)
		if e != nil {
			return nil, e
		}
		if g.InputStamp != input.InputStamp {
			return nil, ports.ErrEpisodeSourceConflict
		}
		p.GroupID = g.GroupID
		p.PartID = g.PartID
		p.SourceHash = g.SourceHash
		p.Source = &g.Source
		p.Rows = g.Rows
		p.HoldReason = g.HoldReason
		p.CanPublish = g.CanMap && (d.Mode != "absolute" || g.CanUseAbsolute)
		if !p.CanPublish && p.HoldReason == "" {
			p.HoldReason = "absolute_sequence_not_proven"
		}
		return p, nil
	}
	units := map[uuid.UUID]ports.EpisodeSourceUnit{}
	for _, u := range input.Units {
		units[u.ID] = u
	}
	numbers := map[int]bool{}
	targets := map[uuid.UUID]bool{}
	for _, entry := range d.Entries {
		u, ok := units[entry.UnitID]
		if !ok || u.Retired || u.Season < 0 || u.Episode < 1 || entry.SourceNumber < 1 || entry.SourceNumber > 9999 || numbers[entry.SourceNumber] || targets[entry.UnitID] {
			p.HoldReason = "manual_mapping_ambiguous_or_retired"
			p.Rows = []ports.EpisodeGroupMappingRow{}
			return p, nil
		}
		numbers[entry.SourceNumber] = true
		targets[entry.UnitID] = true
		p.Rows = append(p.Rows, ports.EpisodeGroupMappingRow{SourceNumber: entry.SourceNumber, EpisodeID: u.EpisodeID, UnitID: u.ID, UnitKey: u.Key, UnitVersion: u.Version})
	}
	sort.Slice(p.Rows, func(i, j int) bool { return p.Rows[i].SourceNumber < p.Rows[j].SourceNumber })
	raw, _ := json.Marshal([]any{d.Mode, p.Rows})
	sum := sha256.Sum256(raw)
	p.SourceHash = hex.EncodeToString(sum[:])
	p.CanPublish = true
	return p, nil
}
func (s *EpisodeMappingService) Publish(ctx context.Context, a *ports.Actor, id uuid.UUID, c ports.EpisodeMappingCommand, key string) (*ports.EpisodeMappingReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() || a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if !c.Confirmed || !validMappingDraft(c.Draft) || len(c.ExpectedInputStamp) != 64 || len(c.ExpectedSourceHash) != 64 || key == "" || len(key) > 160 {
		return nil, ports.ErrInvalidEpisodeMapping
	}
	raw, _ := json.Marshal([]any{"episode.mapping.publish.v1", id, c})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	receipt, e := s.repo.ReadEpisodeMappingReceipt(ctx, a, id, key, hash)
	if e != nil || receipt != nil {
		return receipt, e
	}
	// A same-key peer may commit after our cache miss, invalidating this preview.
	// Its stable receipt takes precedence over a transient/new version observation.
	recoverReceipt := func(cause error) (*ports.EpisodeMappingReceipt, error) {
		r, readErr := s.repo.ReadEpisodeMappingReceipt(ctx, a, id, key, hash)
		if readErr != nil || r != nil {
			return r, readErr
		}
		return nil, cause
	}
	p, e := s.Preview(ctx, a, id, c.Draft)
	if e != nil {
		return recoverReceipt(e)
	}
	if !p.CanPublish {
		return recoverReceipt(ports.ErrInvalidEpisodeMapping)
	}
	if p.InputStamp != c.ExpectedInputStamp || p.SourceHash != c.ExpectedSourceHash {
		return recoverReceipt(ports.ErrEpisodeSourceConflict)
	}
	return s.repo.PublishEpisodeMapping(ctx, a, *p, key, hash)
}

type MappedReleaseExplanation struct {
	IdentityDecision    string                         `json:"identity_decision,omitempty"`
	IdentityReason      string                         `json:"identity_reason,omitempty"`
	QualityDecision     string                         `json:"quality_decision,omitempty"`
	QualityReason       string                         `json:"quality_reason,omitempty"`
	TargetReason        string                         `json:"target_reason,omitempty"`
	CandidateID         *uuid.UUID                     `json:"candidate_id,omitempty"`
	CandidateInputStamp string                         `json:"candidate_input_stamp,omitempty"`
	SourceCurrent       *bool                          `json:"source_current,omitempty"`
	SubscriptionID      uuid.UUID                      `json:"subscription_id"`
	MediaID             uuid.UUID                      `json:"media_id"`
	MappingRevision     int64                          `json:"mapping_revision"`
	IntentVersion       int64                          `json:"intent_version"`
	ParserVersion       string                         `json:"parser_version"`
	InputStamp          string                         `json:"input_stamp"`
	SourceHash          string                         `json:"source_hash"`
	ObservedAt          time.Time                      `json:"observed_at"`
	ValidUntil          *time.Time                     `json:"valid_until,omitempty"`
	CoverageKnown       bool                           `json:"coverage_known"`
	ExecutionAllowed    bool                           `json:"execution_allowed"`
	Reason              string                         `json:"reason"`
	Rows                []ports.EpisodeGroupMappingRow `json:"rows"`
}

func (s *EpisodeMappingService) ExplainRelease(ctx context.Context, a *ports.Actor, id uuid.UUID, name string) (*MappedReleaseExplanation, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if !a.IsAdmin() || a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	if strings.TrimSpace(name) == "" || len(name) > 512 || media.UnsafeCandidateTitle(name) || strings.ContainsAny(name, "/\\") {
		return nil, ports.ErrInvalidEpisodeMapping
	}
	state, e := s.repo.ReadEpisodeMapping(ctx, a, id)
	if e != nil {
		return nil, e
	}
	return explainMappedReleaseState(id, state, name)
}
func explainMappedReleaseState(id uuid.UUID, state *ports.EpisodeMappingState, name string) (*MappedReleaseExplanation, error) {
	out := &MappedReleaseExplanation{SubscriptionID: id, MediaID: state.MediaID, MappingRevision: state.MappingRevision, IntentVersion: state.IntentVersion, ParserVersion: media.EpisodeParserVersion, InputStamp: state.InputStamp, SourceHash: state.SourceHash, ObservedAt: state.ObservedAt, ValidUntil: state.ValidUntil, Rows: []ports.EpisodeGroupMappingRow{}}
	if !state.Current {
		out.Reason = state.HoldReason
		return out, nil
	}
	if media.HasConflictingTVIdentity(name, state.TMDBID) {
		out.Reason = "media_identity_conflict"
		return out, nil
	}
	parsed := media.RecognizeRelease(name)
	if parsed.Reason != "absolute_mapping_required" {
		out.Reason = parsed.Reason
		return out, nil
	}
	rows := make([]media.NumberingRow, 0, len(state.Rows))
	for _, r := range state.Rows {
		rows = append(rows, media.NumberingRow{SourceNumber: r.SourceNumber, EpisodeID: r.EpisodeID, UnitID: r.UnitID, UnitKey: r.UnitKey, UnitVersion: r.UnitVersion})
	}
	resolved, reason := media.ResolveAbsoluteNumbering(parsed, state.Mode, rows)
	out.Reason = reason
	for _, r := range resolved {
		out.Rows = append(out.Rows, ports.EpisodeGroupMappingRow{SourceNumber: r.SourceNumber, EpisodeID: r.EpisodeID, UnitID: r.UnitID, UnitKey: r.UnitKey, UnitVersion: r.UnitVersion})
	}
	out.CoverageKnown = len(out.Rows) > 0
	return out, nil
}

func (s *EpisodeMappingService) Read(ctx context.Context, a *ports.Actor, id uuid.UUID) (*ports.EpisodeMappingState, error) {
	return s.repo.ReadEpisodeMapping(ctx, a, id)
}
