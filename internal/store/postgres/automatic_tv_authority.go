package postgres

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// This fence deliberately cannot be mistaken for a reviewed episode permission.
type automaticTVDemandFence struct {
	ports.DownloadDemandFence
	Goal            string    `json:"goal"`
	Kind            string    `json:"kind"`
	Authority       uuid.UUID `json:"authority"`
	ProtectedDigest string    `json:"protected_digest"`
}

type automaticTVSubmissionPayload struct {
	OperationID uuid.UUID `json:"operation_id"`
	RoundID     uuid.UUID `json:"round_id"`
	Authority   uuid.UUID `json:"automatic_tv_authority"`
	CandidateID uuid.UUID `json:"candidate_id"`
	RootID      uuid.UUID `json:"root_id"`
	ClientID    uuid.UUID `json:"client_id"`
}

func automaticTVAuthorityBytes(a ports.AutomaticTVExecutionAuthority) ([]byte, error) {
	// Normalize PostgreSQL precision and timezone before hashing typed values.
	raw, _, err := automaticTVSnapshotBytes(a.Target)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &a.Target); err != nil {
		return nil, err
	}
	a.IssuedAt = a.IssuedAt.UTC().Truncate(time.Microsecond)
	a.SendValidUntil = a.SendValidUntil.UTC().Truncate(time.Microsecond)
	a.DecisionStamp = ""
	raw, err = json.Marshal(a)
	if err != nil {
		return nil, err
	}
	a.DecisionStamp = automaticTVDigest(raw)
	return json.Marshal(a)
}

func automaticTVNewAuthority(r *automaticTVRoundRow, c ports.AutomaticTVRoundCandidate, op uuid.UUID, rootPathDigest string, now time.Time) (ports.AutomaticTVExecutionAuthority, []byte, error) {
	a := ports.AutomaticTVExecutionAuthority{
		SchemaVersion: "automatic-tv-execution-v1", Kind: "automatic_standard_single_episode_v1",
		OperationID: op, RoundID: r.ID, RoundSnapshotHash: r.SnapshotHash, Target: r.Snapshot, RootPathDigest: rootPathDigest,
		QueryID: c.QueryID, QueryParametersHash: c.QueryParametersHash, CandidateID: c.Candidate.ID,
		IndexerID: c.Candidate.IndexerID, SourceConfigVersion: c.Candidate.SourceConfigVersion,
		SourceStamp: c.QuerySourceStamp, IssuedAt: now, SendValidUntil: now.Add(5 * time.Minute),
	}
	raw, err := automaticTVAuthorityBytes(a)
	if err == nil {
		err = json.Unmarshal(raw, &a)
	}
	return a, raw, err
}

func automaticTVSourceStamp(c ports.SearchCandidate) string {
	hash := ""
	if c.InfoHash != nil {
		hash = *c.InfoHash
	}
	return quality.AcquisitionSourceStamp(c.Title, hash, c.LinkEncrypted, c.QualityProfile)
}

// Membership, identity, coverage and policy remain the store's authorization
// decision even when the injected selector is faulty or mutates its input.
func automaticTVEligible(s ports.AutomaticTVRoundSnapshot, row ports.AutomaticTVRoundCandidate) (int, bool) {
	c := row.Candidate
	if c.ID == uuid.Nil || c.IndexerID == uuid.Nil || row.QueryID == uuid.Nil ||
		c.SourceConfigVersion < 1 || s.IndexerVersions[c.IndexerID.String()] != c.SourceConfigVersion ||
		c.SizeBytes <= 0 || len(c.LinkEncrypted) == 0 || row.QuerySourceStamp != automaticTVSourceStamp(c) {
		return 0, false
	}
	if c.InfoHash != nil {
		if len(*c.InfoHash) != 40 {
			return 0, false
		}
		if _, err := hex.DecodeString(*c.InfoHash); err != nil {
			return 0, false
		}
	}
	var rules quality.PolicyRules
	var profile quality.Profile
	if json.Unmarshal(s.Rules, &rules) != nil || quality.ValidateRules(rules) != nil ||
		len(c.QualityProfile) > 16384 || json.Unmarshal(c.QualityProfile, &profile) != nil || !quality.KnownProfile(profile) {
		return 0, false
	}
	identity := media.DecideAutomaticTVIdentity(c.Title, s.Title, s.OriginalTitle, int(s.Year), s.TMDBID)
	if s.SchemaVersion != "automatic-tv-round-v1" || s.IdentityRuleVersion != media.AutomaticTVIdentityRuleVersion ||
		s.ParserVersion != media.EpisodeParserVersion || identity.State != "eligible" ||
		identity.RuleVersion != s.IdentityRuleVersion || identity.ParserVersion != s.ParserVersion ||
		s.MappingMode != "standard" || s.Season < 1 || s.Episode < 1 || s.EpisodeID < 1 || s.MappingRevision < 0 {
		return 0, false
	}
	units := []media.NumberingUnit{{ID: s.UnitID, EpisodeID: s.EpisodeID, Season: int(s.Season), Episode: int(s.Episode), Key: s.UnitKey, Version: s.UnitVersion}}
	rows, _ := media.ResolveStandardSingleEpisode(media.RecognizeRelease(c.Title), units)
	if len(rows) != 1 || rows[0].UnitID != s.UnitID || rows[0].EpisodeID != s.EpisodeID ||
		rows[0].UnitKey != s.UnitKey || rows[0].UnitVersion != s.UnitVersion ||
		!quality.EvaluateCandidate(rules, c.Title, profile).Passed {
		return 0, false
	}
	p := quality.Policy{ID: s.PolicyID, Revision: s.PolicyRevision, ResolutionOrder: rules.ResolutionOrder, SourceOrder: rules.SourceOrder, PreferHDR: rules.PreferHDR}
	score := p.Score(profile)
	return score, score > 0
}

func automaticTVSelect(selector ports.AutomaticTVSelector, material ports.AutomaticTVSelectionMaterial) (out ports.AutomaticTVSelectionDecision, err error) {
	// JSON clones public material; encrypted links are deliberately omitted by
	// SearchCandidate JSON tags and must be copied and checked separately.
	raw, e := json.Marshal(material)
	if e != nil {
		return out, ports.ErrAcquisitionRoundAborted
	}
	var input ports.AutomaticTVSelectionMaterial
	if json.Unmarshal(raw, &input) != nil {
		return out, ports.ErrAcquisitionRoundAborted
	}
	for i := range input.Candidates {
		input.Candidates[i].Candidate.LinkEncrypted = bytes.Clone(material.Candidates[i].Candidate.LinkEncrypted)
	}
	defer func() {
		if recover() != nil {
			err = ports.ErrAcquisitionRoundAborted
		}
	}()
	out = selector(input)
	for i := range input.Candidates {
		if i >= len(material.Candidates) || !bytes.Equal(input.Candidates[i].Candidate.LinkEncrypted, material.Candidates[i].Candidate.LinkEncrypted) {
			return out, ports.ErrAcquisitionRoundAborted
		}
	}
	after, e := json.Marshal(input)
	if e != nil || !bytes.Equal(raw, after) || out.IdentityRuleVersion != media.AutomaticTVIdentityRuleVersion || out.ParserVersion != media.EpisodeParserVersion {
		return out, ports.ErrAcquisitionRoundAborted
	}
	return out, nil
}
