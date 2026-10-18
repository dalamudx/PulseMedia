package acquisition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var ErrTVSelectionChanged = ports.ErrTVSelectionChanged

type TVSelectionVersion = ports.TVSelectionVersion
type TVSelectionCurrent = ports.TVSelectionCurrent
type TVSelectionWanted = ports.TVSelectionWanted
type TVSelectionCandidateFacts = ports.TVSelectionCandidateFacts
type TVSelectionPack = ports.TVSelectionPack
type TVSelectionEpisode = ports.TVSelectionEpisode
type TVSelectionPreviewInput = ports.TVSelectionPreviewInput

type TVSelectionExclusion struct {
	CandidateID uuid.UUID
	Reason      string
}
type TVSelectionPreview struct {
	Status           string
	ExecutionAllowed bool // always false; no command or reusable execution seal is returned
	Decision         quality.TVSelection
	Excluded         []TVSelectionExclusion
	Version          TVSelectionVersion // common current snapshot, never execution authority
}

// PreviewTVSelection is a concrete, side-effect-free usecase over explicit native
// evidence. It performs no I/O and is not an HTTP trust boundary. In particular,
// caller assertions cannot replace DB authorization, transactional revalidation,
// or the separate complete-set/single-episode acquisition UoWs.
func PreviewTVSelection(ctx context.Context, actor *ports.Actor, in TVSelectionPreviewInput) (*TVSelectionPreview, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if actor == nil {
		return nil, ports.ErrUnauthorized
	}
	actorID, err := uuid.Parse(actor.ID)
	if err != nil || actorID == uuid.Nil || actor.Type != ports.ActorTypeUser || !actor.IsAdmin() || actor.Scopes != nil {
		return nil, ports.ErrForbidden
	}
	v := in.Current.Version
	if !in.Current.AccountAuthorized || !in.Current.ObjectAuthorized || v.ActorID != actorID {
		return nil, ports.ErrForbidden
	}
	if !validSelectionVersion(v) || v != in.Expected {
		return nil, ErrTVSelectionChanged
	}
	if in.Policy.ID != v.PolicyID || in.Policy.Revision != v.PolicyRevision {
		return nil, ErrTVSelectionChanged
	}
	if quality.ValidateRules(in.Policy.Rules) != nil {
		return nil, ports.ErrInvalidPolicy
	}
	// Validate explicit policy even when there are no alternatives or live guards fail.
	policyStamp, err := TVSelectionPolicyStamp(in.PackPolicy)
	if err != nil {
		return nil, err
	}
	if policyStamp != v.SelectionPolicyStamp {
		return nil, ErrTVSelectionChanged
	}
	out := &TVSelectionPreview{Version: v, Status: "preview_not_executable", Decision: quality.TVSelection{Kind: "hold", CandidateIDs: []string{}}, Excluded: []TVSelectionExclusion{}}
	if !in.Current.Active || !in.Current.RootReady || !in.Current.EndpointsReady {
		out.Decision.Reason = "current_target_unavailable"
		return out, nil
	}
	season := in.Season
	if season.Season < 1 || season.SourceTMDBID < 1 || season.SourceTMDBID != in.Current.SourceTMDBID || season.MediaVersion != v.MediaVersion || !selectionDigest(season.ContentHash) || len(season.Units) < 1 || len(season.Units) > 1000 || len(in.Current.Canonical) != len(season.Units) || len(in.Wanted) != len(season.Units) {
		return nil, ErrTVSelectionChanged
	}
	units := map[uuid.UUID]ports.SeasonMetadataUnit{}
	episodes := map[int]bool{}
	providerIDs := map[int64]bool{}
	expected := []int{}
	for _, u := range season.Units {
		if u.UnitID == uuid.Nil || u.UnitVersion < 1 || u.TMDBEpisodeID < 1 || u.EpisodeNumber < 1 || u.EpisodeNumber > 9999 || units[u.UnitID].UnitID != uuid.Nil || episodes[u.EpisodeNumber] || providerIDs[u.TMDBEpisodeID] {
			return nil, ErrTVSelectionChanged
		}
		units[u.UnitID] = u
		episodes[u.EpisodeNumber] = true
		providerIDs[u.TMDBEpisodeID] = true
		expected = append(expected, u.EpisodeNumber)
	}
	seen := map[uuid.UUID]bool{}
	for _, u := range in.Current.Canonical {
		s, ok := units[u.ID]
		if !ok || seen[u.ID] || u.Retired || u.Version != s.UnitVersion || u.EpisodeID != s.TMDBEpisodeID || u.Season != season.Season || u.Episode != s.EpisodeNumber || u.Key != media.FormatEpisodeUnitKey(season.Season, s.EpisodeNumber) {
			return nil, ErrTVSelectionChanged
		}
		seen[u.ID] = true
	}
	wanted := map[uuid.UUID]TVSelectionWanted{}
	missing := []int{}
	packEligible := len(units) >= 2
	for _, w := range in.Wanted {
		u, ok := units[w.UnitID]
		if !ok || wanted[w.UnitID].UnitID != uuid.Nil || w.UnitVersion != u.UnitVersion {
			return nil, ErrTVSelectionChanged
		}
		wanted[w.UnitID] = w
		if w.Selected && w.Missing {
			missing = append(missing, u.EpisodeNumber)
		}
		if !w.Selected || !w.Missing || !w.Aired || !w.Unprotected {
			packEligible = false
		}
	}
	allIDs := map[uuid.UUID]bool{}
	checkID := func(id uuid.UUID) bool {
		if id == uuid.Nil || allIDs[id] {
			return false
		}
		allIDs[id] = true
		return true
	}
	for _, p := range in.Packs {
		if !checkID(p.Facts.CandidateID) {
			return nil, quality.ErrInvalidTVSelection
		}
	}
	for _, e := range in.Episodes {
		if !checkID(e.Facts.CandidateID) {
			return nil, quality.ErrInvalidTVSelection
		}
	}
	exclusion := func(id uuid.UUID, reason string) {
		out.Excluded = append(out.Excluded, TVSelectionExclusion{id, reason})
	}
	factsOK := func(f TVSelectionCandidateFacts) bool {
		return f.Version == v && f.EvidenceCurrent && f.StandardNumbering && selectionDigest(f.CurrentInputStamp) && f.Name != "" && len(f.Name) <= 512 && !media.UnsafeCandidateTitle(f.Name)
	}
	rowOK := func(r media.NumberingRow) bool {
		u, ok := units[r.UnitID]
		return ok && r.UnitVersion == u.UnitVersion && r.EpisodeID == u.TMDBEpisodeID && r.UnitKey == media.FormatEpisodeUnitKey(season.Season, u.EpisodeNumber) && r.SourceNumber == u.EpisodeNumber
	}
	packs := []quality.PackCandidate{}
	for _, p := range in.Packs {
		f, r := p.Facts, p.Review
		if !factsOK(f) || r.ID == uuid.Nil || r.SubscriptionID != v.SubscriptionID || r.CandidateID != f.CandidateID || r.Status != "reviewed_not_executable" || r.InputStamp != f.CurrentInputStamp || !selectionDigest(p.CurrentMaterialStamp) || r.MaterialStamp != p.CurrentMaterialStamp {
			exclusion(f.CandidateID, "pack_review_changed")
			continue
		}
		if !packEligible {
			exclusion(f.CandidateID, "full_set_unavailable")
			continue
		}
		covered := []int{}
		seenRows := map[uuid.UUID]bool{}
		valid := len(f.Rows) == len(units)
		for _, row := range f.Rows {
			if !rowOK(row) || seenRows[row.UnitID] {
				valid = false
			}
			seenRows[row.UnitID] = true
			covered = append(covered, row.SourceNumber)
		}
		candidate := quality.PackCandidate{ID: f.CandidateID.String(), Season: season.Season, TotalSeasonEps: len(units), ExpectedEpisodes: expected, CoveredEpisodes: covered, SeasonMetadataKnown: true, CoverageKnown: valid}
		if !candidate.IsCompleteSeason() {
			exclusion(f.CandidateID, "incomplete_pack_coverage")
			continue
		}
		q := quality.EvaluateCandidate(in.Policy.Rules, f.Name, f.Quality)
		if !q.Passed {
			exclusion(f.CandidateID, "quality_rejected")
			continue
		}
		candidate.Score = q.Score
		packs = append(packs, candidate)
	}
	singles := []quality.EpisodeSelectionCandidate{}
	for _, e := range in.Episodes {
		f, r := e.Facts, e.Review
		if !factsOK(f) || r.ID == uuid.Nil || r.SubscriptionID != v.SubscriptionID || r.CandidateID != f.CandidateID || r.Status != "reviewed_not_executable" || r.InputStamp != f.CurrentInputStamp || len(f.Rows) != 1 || f.Rows[0].UnitID != r.UnitID || !rowOK(f.Rows[0]) {
			exclusion(f.CandidateID, "episode_review_changed")
			continue
		}
		w := wanted[r.UnitID]
		if !w.Selected || !w.Missing || !w.Aired || !w.Unprotected {
			exclusion(f.CandidateID, "episode_target_unavailable")
			continue
		}
		q := quality.EvaluateCandidate(in.Policy.Rules, f.Name, f.Quality)
		if !q.Passed {
			exclusion(f.CandidateID, "quality_rejected")
			continue
		}
		singles = append(singles, quality.EpisodeSelectionCandidate{ID: f.CandidateID.String(), Episode: units[r.UnitID].EpisodeNumber, Score: q.Score})
	}
	out.Decision, err = quality.ChooseTVSelection(in.PackPolicy, expected, missing, packs, singles)
	if err != nil {
		return nil, err
	}
	sort.Slice(out.Excluded, func(i, j int) bool {
		return out.Excluded[i].CandidateID.String() < out.Excluded[j].CandidateID.String()
	})
	return out, nil
}

func selectionDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return false
	}
	return hex.EncodeToString(b) == s
}
func validSelectionVersion(v TVSelectionVersion) bool {
	return v.ActorID != uuid.Nil && v.SubscriptionID != uuid.Nil && v.MediaID != uuid.Nil && v.IntentVersion > 0 && v.MediaVersion > 0 && v.PolicyID != "" && v.PolicyRevision > 0 && selectionDigest(v.ScopeStamp) && selectionDigest(v.ConfigurationStamp) && selectionDigest(v.SelectionPolicyStamp)
}

// TVSelectionPolicyStamp binds explicit preview policy inputs without adding them
// to published quality JSON. It is only an input identity, never an execution seal.
func TVSelectionPolicyStamp(policy quality.PackPolicy) (string, error) {
	if err := quality.ValidateTVSelectionPolicy(policy); err != nil {
		return "", err
	}
	raw, err := json.Marshal([]any{"tv.selection.policy.v1", policy.Mode, policy.MinMissingRatio})
	if err != nil {
		return "", quality.ErrInvalidTVSelection
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
