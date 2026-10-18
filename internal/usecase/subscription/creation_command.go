package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// SubscriptionCreationService prepares observations before the repository's
// atomic, independently authorized publication boundary.
type SubscriptionCreationService struct {
	repo      ports.SubscriptionCreationRepository
	provider  ports.MetadataProvider
	episodes  ports.TVEpisodeProvider
	protector ports.SecretProtector
}

func NewSubscriptionCreationService(repo ports.SubscriptionCreationRepository, provider ports.MetadataProvider, episodes ports.TVEpisodeProvider, protector ports.SecretProtector) *SubscriptionCreationService {
	return &SubscriptionCreationService{repo: repo, provider: provider, episodes: episodes, protector: protector}
}

func (s *SubscriptionCreationService) Create(ctx context.Context, actor *ports.Actor, input ports.SubscriptionCreationInput, key string) (*ports.SubscriptionCreationReceipt, error) {
	if err := intentActor(actor); err != nil {
		return nil, err
	}
	a := *actor
	owner, _ := uuid.Parse(a.ID)
	in, err := canonicalCreationInput(input)
	if err != nil {
		return nil, err
	}
	if in.UserID != nil && *in.UserID != owner && a.Role != ports.RoleAdmin {
		return nil, ports.ErrForbidden
	}
	if !validCreationKey(key) {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	if s == nil || s.repo == nil || s.protector == nil {
		return nil, ports.ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	hash, err := s.protector.DigestSecret(append([]byte("subscription.creation.v1:"), raw...))
	if err != nil || hash == "" {
		return nil, ports.ErrUnavailable
	}
	identity := ports.SubscriptionCreationIdentity{Key: key, RequestHash: hash}
	// No collaborator receives the private canonical snapshot used by the callback.
	readActor := a
	observed, receipt, err := s.repo.ReadSubscriptionCreationContext(ctx, &readActor, cloneCreationInput(in), identity)
	if err != nil {
		return nil, creationError(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if receipt != nil {
		// The repository authorizes the historical object and fingerprint before
		// recovery; it may return no latest-state observation on this path.
		expectedOwner := owner
		if in.UserID != nil {
			expectedOwner = *in.UserID
		}
		if receipt.Subscription.UserID != expectedOwner {
			return nil, ports.ErrForbidden
		}
		return cloneCreationReceipt(receipt), nil
	}
	if err = validateCreationContext(in, owner, a.Role == ports.RoleAdmin, observed); err != nil {
		return nil, err
	}
	observed.Existing = cloneCreationRecord(observed.Existing)
	if in.ExpectedIntentVersion != nil && (observed.Existing == nil || observed.Existing.IntentVersion != *in.ExpectedIntentVersion) {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	network, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	var prepared *ports.PreparedSubscriptionCreationMetadata
	if in.TMDBID != nil || (observed.MediaType == "tv" && observed.TMDBID > 0) {
		prepared, err = PrepareSubscriptionCreationMetadata(network, s.provider, s.episodes, observed.MediaType, observed.TMDBID, slices.Clone(in.Seasons), in.IncludeSpecials)
		if err != nil {
			return nil, creationError(network, err)
		}
	}
	if err = network.Err(); err != nil {
		return nil, err
	}
	callbackInput := cloneCreationInput(in)
	rootID, mediaID, mediaType := observed.RootID, observed.MediaID, observed.MediaType
	decide := func(frozen ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
		if frozen.MediaType != mediaType || (mediaID != uuid.Nil && frozen.MediaID != mediaID) {
			return ports.SubscriptionCreateDecision{}, ports.ErrSubscriptionBindingsConflict
		}
		return DecideSubscriptionCreation(callbackInput, rootID, frozen)
	}
	commitActor := a
	receipt, err = s.repo.CommitSubscriptionCreation(network, &commitActor, cloneCreationInput(in), observed, cloneCreationMetadata(prepared), identity, decide)
	if err != nil {
		return nil, creationError(network, err)
	}
	if receipt == nil {
		return nil, ports.ErrUnavailable
	}
	return cloneCreationReceipt(receipt), nil
}

// Receipt recovers the original command result without reconstructing its body
// or requiring metadata providers or the request-digest protector.
func (s *SubscriptionCreationService) Receipt(ctx context.Context, actor *ports.Actor, key string) (*ports.SubscriptionCreationReceipt, error) {
	if err := intentActor(actor); err != nil {
		return nil, err
	}
	if !validCreationKey(key) {
		return nil, ports.ErrInvalidSubscriptionBindings
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a := *actor
	out, err := s.repo.ReadSubscriptionCreationReceipt(ctx, &a, key)
	if err != nil {
		return nil, creationError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, ports.ErrUnavailable
	}
	return cloneCreationReceipt(out), nil
}

func validCreationKey(key string) bool {
	if len(key) < 1 || len(key) > 160 {
		return false
	}
	for _, c := range []byte(key) {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':'
		if !allowed {
			return false
		}
	}
	return true
}

func canonicalCreationInput(input ports.SubscriptionCreationInput) (ports.SubscriptionCreationInput, error) {
	in := cloneCreationInput(input)
	invalid := func() (ports.SubscriptionCreationInput, error) {
		return ports.SubscriptionCreationInput{}, ports.ErrInvalidSubscriptionBindings
	}
	if (in.MediaID == nil) == (in.TMDBID == nil) {
		return invalid()
	}
	if in.MediaID != nil && *in.MediaID == uuid.Nil || in.TMDBID != nil && *in.TMDBID <= 0 {
		return invalid()
	}
	if in.UserID != nil && *in.UserID == uuid.Nil || in.RootID != nil && *in.RootID == uuid.Nil || in.DownloaderID != nil && *in.DownloaderID == uuid.Nil {
		return invalid()
	}
	if in.TMDBID != nil && in.MediaType == "" {
		in.MediaType = "movie"
	}
	if in.MediaType != "" && in.MediaType != "movie" && in.MediaType != "tv" {
		return invalid()
	}
	if in.ScopeType == "" {
		in.ScopeType = "all"
	}
	if in.ScopeType != "all" && in.ScopeType != "selected" {
		return invalid()
	}
	if in.QualityPolicyID == "" {
		in.QualityPolicyID = "default"
	}
	if len(in.QualityPolicyID) > 128 || strings.TrimSpace(in.QualityPolicyID) != in.QualityPolicyID {
		return invalid()
	}
	if in.ExpectedIntentVersion != nil && *in.ExpectedIntentVersion <= 0 {
		return invalid()
	}
	if len(in.IndexerIDs) > 64 || len(in.Seasons) > 64 || len(in.EpisodeKeys) > 20000 || len(in.SelectedUnitIDs) > 20000 {
		return invalid()
	}
	bindings := map[uuid.UUID]bool{}
	for _, id := range in.IndexerIDs {
		if id == uuid.Nil || bindings[id] {
			return invalid()
		}
		bindings[id] = true
	}
	sort.Ints(in.Seasons)
	for i, n := range in.Seasons {
		if n < 0 || n > 999 || n == 0 && !in.IncludeSpecials || i > 0 && n == in.Seasons[i-1] {
			return invalid()
		}
	}
	sort.Strings(in.EpisodeKeys)
	for i, key := range in.EpisodeKeys {
		if !creationEpisodeKey(key) || i > 0 && key == in.EpisodeKeys[i-1] {
			return invalid()
		}
	}
	sort.Slice(in.SelectedUnitIDs, func(i, j int) bool { return in.SelectedUnitIDs[i].String() < in.SelectedUnitIDs[j].String() })
	for i, id := range in.SelectedUnitIDs {
		if id == uuid.Nil || i > 0 && id == in.SelectedUnitIDs[i-1] {
			return invalid()
		}
	}
	if in.ScopeType == "all" && len(in.EpisodeKeys)+len(in.SelectedUnitIDs) > 0 || in.ScopeType == "selected" && len(in.EpisodeKeys)+len(in.SelectedUnitIDs) == 0 {
		return invalid()
	}
	if in.MediaType == "movie" && (len(in.Seasons)+len(in.EpisodeKeys) > 0 || in.IncludeSpecials) {
		return invalid()
	}
	// Only indexer nil versus [] carries distinct intent semantics.
	if in.Seasons == nil {
		in.Seasons = []int{}
	}
	if in.EpisodeKeys == nil {
		in.EpisodeKeys = []string{}
	}
	if in.SelectedUnitIDs == nil {
		in.SelectedUnitIDs = []uuid.UUID{}
	}
	return in, nil
}

func creationEpisodeKey(key string) bool {
	if len(key) < 6 || len(key) > 10 {
		return false
	}
	var season, episode int
	if _, err := fmt.Sscanf(key, "S%dE%d", &season, &episode); err != nil {
		return false
	}
	return season >= 0 && season <= 999 && episode > 0 && episode <= 9999 && key == media.FormatEpisodeUnitKey(season, episode)
}

func validateCreationContext(in ports.SubscriptionCreationInput, actorID uuid.UUID, admin bool, f ports.SubscriptionCreationContext) error {
	owner := actorID
	if in.UserID != nil {
		owner = *in.UserID
	}
	if f.OwnerID != owner || (!admin && f.OwnerID != actorID) {
		return ports.ErrForbidden
	}
	if f.RootID == uuid.Nil || in.RootID != nil && *in.RootID != f.RootID {
		return ports.ErrInvalidSubscriptionBindings
	}
	if f.MediaType != "movie" && f.MediaType != "tv" || in.MediaType != "" && in.MediaType != f.MediaType {
		return ports.ErrInvalidSubscriptionBindings
	}
	if f.TMDBID < 0 || in.TMDBID != nil && *in.TMDBID != f.TMDBID || in.MediaID != nil && *in.MediaID != f.MediaID {
		return ports.ErrSubscriptionBindingsConflict
	}
	if f.MediaID == uuid.Nil && (in.MediaID != nil || f.MediaVersion != 0) || f.MediaID != uuid.Nil && f.MediaVersion <= 0 {
		return ports.ErrSubscriptionBindingsConflict
	}
	if f.MediaType == "movie" && (len(in.Seasons)+len(in.EpisodeKeys) > 0 || in.IncludeSpecials) {
		return ports.ErrInvalidSubscriptionBindings
	}
	if f.Existing != nil && (f.Existing.UserID != owner || f.Existing.MediaID != f.MediaID || f.Existing.RootID != f.RootID || f.Existing.IntentVersion <= 0) {
		return ports.ErrSubscriptionBindingsConflict
	}
	return nil
}

// DecideSubscriptionCreation evaluates only locked, actual units. Scope exclusion
// is separate from user-skipped provenance, which remains owned by the repository.
func DecideSubscriptionCreation(input ports.SubscriptionCreationInput, rootID uuid.UUID, frozen ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
	var out ports.SubscriptionCreateDecision
	in, err := canonicalCreationInput(input)
	if err != nil {
		return out, err
	}
	if rootID == uuid.Nil || frozen.MediaID == uuid.Nil || frozen.RootHealth != "online" || frozen.RootMediaType != frozen.MediaType || frozen.PolicyRevision <= 0 {
		return out, ports.ErrInvalidSubscriptionBindings
	}
	if in.RootID != nil && *in.RootID != rootID || in.MediaID != nil && *in.MediaID != frozen.MediaID || in.MediaType != "" && in.MediaType != frozen.MediaType {
		return out, ports.ErrSubscriptionBindingsConflict
	}
	if frozen.MediaType != "tv" && frozen.MediaType != "movie" || frozen.MediaType == "movie" && (len(in.Seasons)+len(in.EpisodeKeys) > 0 || in.IncludeSpecials) {
		return out, ports.ErrInvalidSubscriptionBindings
	}
	existing := cloneCreationRecord(frozen.Existing)
	if in.ExpectedIntentVersion != nil && (existing == nil || existing.IntentVersion != *in.ExpectedIntentVersion) {
		return out, ports.ErrSubscriptionBindingsConflict
	}
	if existing != nil && (existing.MediaID != frozen.MediaID || existing.RootID != rootID || in.UserID != nil && existing.UserID != *in.UserID) {
		return out, ports.ErrSubscriptionBindingsConflict
	}
	cmd := ports.CreateSubscriptionCommand{UserID: in.UserID, MediaID: frozen.MediaID, RootID: rootID, QualityPolicyID: in.QualityPolicyID, QualityPolicyRevision: frozen.PolicyRevision, IndexerIDs: in.IndexerIDs, DownloaderID: in.DownloaderID, ScopeType: in.ScopeType, IncludeSpecials: in.IncludeSpecials, Seasons: in.Seasons, EpisodeKeys: in.EpisodeKeys, SelectedUnitIDs: in.SelectedUnitIDs}
	if existing != nil {
		if cmd.IndexerIDs == nil {
			cmd.IndexerIDs = slices.Clone(existing.IndexerIDs)
		}
		if cmd.DownloaderID == nil {
			cmd.DownloaderID = creationPtr(existing.DownloaderID)
		}
	}
	if cmd.IndexerIDs == nil {
		cmd.IndexerIDs = []uuid.UUID{}
	}
	if len(cmd.IndexerIDs) > 64 {
		return out, ports.ErrInvalidSubscriptionBindings
	}
	seenBindings := map[uuid.UUID]bool{}
	for _, id := range cmd.IndexerIDs {
		if id == uuid.Nil || seenBindings[id] {
			return out, ports.ErrInvalidSubscriptionBindings
		}
		seenBindings[id] = true
	}
	if cmd.DownloaderID != nil && *cmd.DownloaderID == uuid.Nil {
		return out, ports.ErrInvalidSubscriptionBindings
	}
	seasons := map[int]bool{}
	for _, n := range cmd.Seasons {
		seasons[n] = true
	}
	keys := map[string]bool{}
	for _, key := range cmd.EpisodeKeys {
		keys[key] = true
	}
	selected := map[uuid.UUID]bool{}
	for _, id := range cmd.SelectedUnitIDs {
		selected[id] = true
	}
	available := map[uuid.UUID]bool{}
	seenKeys := map[string]bool{}
	for _, unit := range frozen.Units {
		if unit.Retired || unit.ID == uuid.Nil {
			continue
		}
		valid := false
		if frozen.MediaType == "movie" {
			valid = unit.UnitKey == "movie" && unit.SeasonNumber == nil && unit.EpisodeNumber == nil
		} else if unit.SeasonNumber != nil && unit.EpisodeNumber != nil {
			sn, en := int(*unit.SeasonNumber), int(*unit.EpisodeNumber)
			valid = creationEpisodeKey(unit.UnitKey) && unit.UnitKey == media.FormatEpisodeUnitKey(sn, en) && (sn != 0 || cmd.IncludeSpecials) && (len(seasons) == 0 || seasons[sn])
		}
		if !valid {
			continue
		}
		if available[unit.ID] || seenKeys[unit.UnitKey] {
			return out, ports.ErrInvalidSubscriptionBindings
		}
		available[unit.ID], seenKeys[unit.UnitKey] = true, true
		if keys[unit.UnitKey] {
			selected[unit.ID] = true
			delete(keys, unit.UnitKey)
		}
	}
	if len(keys) > 0 {
		return out, ports.ErrInvalidSubscriptionBindings
	}
	for id := range selected {
		if !available[id] {
			return out, ports.ErrInvalidSubscriptionBindings
		}
	}
	cmd.SelectedUnitIDs = make([]uuid.UUID, 0, len(selected))
	for id := range selected {
		cmd.SelectedUnitIDs = append(cmd.SelectedUnitIDs, id)
	}
	sort.Slice(cmd.SelectedUnitIDs, func(i, j int) bool { return cmd.SelectedUnitIDs[i].String() < cmd.SelectedUnitIDs[j].String() })
	if frozen.MediaType == "movie" && len(available) == 0 {
		return out, ports.ErrInvalidSubscriptionBindings
	}
	if existing != nil && in.ExpectedIntentVersion == nil {
		oldSeasons := slices.Clone(existing.Seasons)
		sort.Ints(oldSeasons)
		oldSelected := slices.Clone(existing.SelectedUnitIDs)
		sort.Slice(oldSelected, func(i, j int) bool { return oldSelected[i].String() < oldSelected[j].String() })
		if existing.QualityPolicyID != cmd.QualityPolicyID || existing.ScopeType != cmd.ScopeType || existing.IncludeSpecials != cmd.IncludeSpecials || !slices.Equal(existing.IndexerIDs, cmd.IndexerIDs) || !creationPtrEqual(existing.DownloaderID, cmd.DownloaderID) || !slices.Equal(oldSeasons, cmd.Seasons) || !slices.Equal(oldSelected, cmd.SelectedUnitIDs) {
			return out, ports.ErrSubscriptionBindingsConflict
		}
		cmd.QualityPolicyRevision = existing.QualityPolicyRevision
	}
	wanted := make([]ports.SubscriptionWantedDecision, 0, len(available))
	for _, unit := range frozen.Units {
		if available[unit.ID] {
			wanted = append(wanted, ports.SubscriptionWantedDecision{UnitID: unit.ID, ScopeExcluded: cmd.ScopeType == "selected" && !selected[unit.ID]})
		}
	}
	return ports.SubscriptionCreateDecision{Command: cmd, Wanted: wanted}, nil
}

func creationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrInvalidSubscriptionBindings, ports.ErrSubscriptionBindingsConflict, ports.ErrNoSubscriptionRoot, ports.ErrAmbiguousSubscriptionRoot, ports.ErrBadResponse, ports.ErrTimeout} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}

func creationPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func creationPtrEqual[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func cloneCreationInput(in ports.SubscriptionCreationInput) ports.SubscriptionCreationInput {
	in.UserID = creationPtr(in.UserID)
	in.MediaID = creationPtr(in.MediaID)
	in.TMDBID = creationPtr(in.TMDBID)
	in.RootID = creationPtr(in.RootID)
	in.DownloaderID = creationPtr(in.DownloaderID)
	in.ExpectedIntentVersion = creationPtr(in.ExpectedIntentVersion)
	in.IndexerIDs = slices.Clone(in.IndexerIDs)
	in.Seasons = slices.Clone(in.Seasons)
	in.EpisodeKeys = slices.Clone(in.EpisodeKeys)
	in.SelectedUnitIDs = slices.Clone(in.SelectedUnitIDs)
	return in
}
func cloneCreationRecord(in *ports.SubscriptionRecord) *ports.SubscriptionRecord {
	if in == nil {
		return nil
	}
	out := *in
	out.IndexerIDs = slices.Clone(in.IndexerIDs)
	out.Seasons = slices.Clone(in.Seasons)
	out.SelectedUnitIDs = slices.Clone(in.SelectedUnitIDs)
	out.DownloaderID = creationPtr(in.DownloaderID)
	out.EpisodeMappingRevision = creationPtr(in.EpisodeMappingRevision)
	return &out
}
func cloneCreationReceipt(in *ports.SubscriptionCreationReceipt) *ports.SubscriptionCreationReceipt {
	if in == nil {
		return nil
	}
	out := *in
	out.Subscription = *cloneCreationRecord(&in.Subscription)
	out.Wanted = slices.Clone(in.Wanted)
	return &out
}
func cloneCreationMetadata(in *ports.PreparedSubscriptionCreationMetadata) *ports.PreparedSubscriptionCreationMetadata {
	if in == nil {
		return nil
	}
	out := *in
	out.Media.Year = creationPtr(in.Media.Year)
	out.Media.Seasons = slices.Clone(in.Media.Seasons)
	out.Units = slices.Clone(in.Units)
	for i := range out.Units {
		out.Units[i].SeasonNumber = creationPtr(out.Units[i].SeasonNumber)
		out.Units[i].EpisodeNumber = creationPtr(out.Units[i].EpisodeNumber)
	}
	out.Seasons = slices.Clone(in.Seasons)
	for i := range out.Seasons {
		out.Seasons[i].Episodes = slices.Clone(in.Seasons[i].Episodes)
		for j := range out.Seasons[i].Episodes {
			e := &out.Seasons[i].Episodes[j]
			e.AirDate = creationPtr(e.AirDate)
			e.AbsoluteNumber = creationPtr(e.AbsoluteNumber)
		}
	}
	return &out
}
