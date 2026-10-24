package acquisition

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// SelectAutomaticTVEpisode selects observation material only. The caller must
// independently revalidate query membership and all execution fences atomically.
func SelectAutomaticTVEpisode(input ports.AutomaticTVSelectionMaterial) ports.AutomaticTVSelectionDecision {
	out := ports.AutomaticTVSelectionDecision{
		Score: -1, Reason: "no_qualified_candidate",
		IdentityRuleVersion: media.AutomaticTVIdentityRuleVersion,
		ParserVersion:       media.EpisodeParserVersion,
	}
	rules, valid := automaticTVSelectionRules(input.Snapshot)
	if !valid {
		out.Reason = "unproven_target"
		return out
	}
	s := input.Snapshot
	unit := media.NumberingUnit{ID: s.UnitID, EpisodeID: s.EpisodeID, Season: int(s.Season),
		Episode: int(s.Episode), Key: s.UnitKey, Version: s.UnitVersion}

	// Contradictory copies of an immutable candidate cannot compete by input order.
	// Different queries may carry the same current source proof.
	type facts struct {
		stamp, proof string
		indexer      uuid.UUID
		config, size int64
		seeders      int32
	}
	seen := make(map[uuid.UUID]facts, len(input.Candidates))
	conflicts := make(map[uuid.UUID]bool)
	for _, row := range input.Candidates {
		c := row.Candidate
		f := facts{automaticTVSelectionSourceStamp(c), row.QuerySourceStamp, c.IndexerID, c.SourceConfigVersion, c.SizeBytes, c.Seeders}
		if previous, ok := seen[c.ID]; ok && previous != f {
			conflicts[c.ID] = true
		}
		seen[c.ID] = f
	}
	var bestSeeders int32
	bestSelectionScore := 0
	for _, row := range input.Candidates {
		c := row.Candidate
		if conflicts[c.ID] || c.ID == uuid.Nil || c.IndexerID == uuid.Nil || row.QueryID == uuid.Nil ||
			c.SourceConfigVersion < 1 || s.IndexerVersions[c.IndexerID.String()] != c.SourceConfigVersion ||
			!automaticTVSelectionDigest(row.QueryParametersHash) || row.QuerySourceStamp == "" ||
			row.QuerySourceStamp != seen[c.ID].stamp || c.SizeBytes <= 0 || len(c.LinkEncrypted) == 0 {
			continue
		}
		if c.InfoHash != nil {
			if len(*c.InfoHash) != 40 {
				continue
			}
			if _, err := hex.DecodeString(*c.InfoHash); err != nil {
				continue
			}
		}
		var profile quality.Profile
		if len(c.QualityProfile) > 16384 || json.Unmarshal(c.QualityProfile, &profile) != nil || !quality.KnownProfile(profile) {
			continue
		}
		identity := media.DecideAutomaticTVIdentity(c.Title, s.Title, s.OriginalTitle, int(s.Year), s.TMDBID)
		if identity.State != "eligible" || identity.RuleVersion != s.IdentityRuleVersion || identity.ParserVersion != s.ParserVersion {
			continue
		}
		rows, _ := media.ResolveStandardSingleEpisode(media.RecognizeRelease(c.Title), []media.NumberingUnit{unit})
		if len(rows) != 1 || rows[0].UnitID != s.UnitID || rows[0].UnitKey != s.UnitKey ||
			rows[0].EpisodeID != s.EpisodeID || rows[0].UnitVersion != s.UnitVersion ||
			rows[0].UnitVersion < 1 || rows[0].EpisodeID < 1 {
			continue
		}
		evaluation := quality.EvaluateCandidate(rules, c.Title, profile)
		if !evaluation.Passed {
			continue
		}
		selectionScore := evaluation.SelectionScore()
		if out.CandidateID == nil || selectionScore > bestSelectionScore || selectionScore == bestSelectionScore &&
			(c.Seeders > bestSeeders || c.Seeders == bestSeeders && bytes.Compare(c.ID[:], out.CandidateID[:]) < 0) {
			id := c.ID
			out.CandidateID, out.Score, out.Reason = &id, evaluation.Score, "selected"
			bestSeeders = c.Seeders
			bestSelectionScore = selectionScore
		}
	}
	return out
}

func automaticTVSelectionRules(s ports.AutomaticTVRoundSnapshot) (quality.PolicyRules, bool) {
	var rules quality.PolicyRules
	if s.SchemaVersion != "automatic-tv-round-v1" || s.IdentityRuleVersion != media.AutomaticTVIdentityRuleVersion ||
		s.ParserVersion != media.EpisodeParserVersion || s.SubscriptionID == uuid.Nil || s.OwnerID == uuid.Nil ||
		s.WantedID == uuid.Nil || s.MediaID == uuid.Nil || s.UnitID == uuid.Nil || s.RootID == uuid.Nil || s.ClientID == uuid.Nil ||
		s.IntentVersion < 1 || s.MediaVersion < 1 || s.UnitVersion < 1 || s.PolicyRevision < 1 ||
		s.MappingRevision < 0 || s.RootGeneration < 1 || s.RootHealthRevision < 0 || !automaticTVSelectionDigest(s.ClientStamp) ||
		s.TMDBID < 1 || s.Year < 1 || s.Year > 9999 || strings.TrimSpace(s.Title+s.OriginalTitle) == "" ||
		s.MappingMode != "standard" || s.Season < 1 || s.Season > 999 || s.Episode < 1 || s.Episode > 9999 ||
		s.EpisodeID < 1 || s.UnitKey != media.FormatEpisodeUnitKey(int(s.Season), int(s.Episode)) ||
		s.ObservedAt.IsZero() || s.AirDate.IsZero() || s.AirDate.UTC().Format("2006-01-02") > s.ObservedAt.UTC().Format("2006-01-02") ||
		s.PolicyID == "" || len(s.PolicyID) > 128 || !utf8.ValidString(s.PolicyID) || strings.TrimSpace(s.PolicyID) != s.PolicyID ||
		strings.ContainsFunc(s.PolicyID, unicode.IsControl) || len(s.Rules) == 0 || len(s.Rules) > 65536 || len(s.IndexerVersions) == 0 {
		return rules, false
	}
	for key, version := range s.IndexerVersions {
		id, err := uuid.Parse(key)
		if err != nil || id == uuid.Nil || id.String() != key || version < 1 {
			return rules, false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(s.Rules))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&rules) != nil || quality.ValidateRules(rules) != nil {
		return rules, false
	}
	var extra any
	return rules, decoder.Decode(&extra) == io.EOF
}

func automaticTVSelectionSourceStamp(c ports.SearchCandidate) string {
	infoHash := ""
	if c.InfoHash != nil {
		infoHash = *c.InfoHash
	}
	return quality.AcquisitionSourceStamp(c.Title, infoHash, c.LinkEncrypted, c.QualityProfile)
}

func automaticTVSelectionDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
