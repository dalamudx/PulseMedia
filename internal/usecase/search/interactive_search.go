package search

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// InteractiveSearchService prepares actor-owned observations. Current account,
// object scope, immutable policy and command receipt authority lives in repo.
// It performs no network I/O and owns no background work.
type InteractiveSearchService struct {
	repo    ports.InteractiveSearchRepository
	secrets ports.SecretProtector
}

func NewInteractiveSearchService(repo ports.InteractiveSearchRepository, secrets ports.SecretProtector) *InteractiveSearchService {
	return &InteractiveSearchService{repo: repo, secrets: secrets}
}

func (s *InteractiveSearchService) Create(ctx context.Context, actor *ports.Actor, input ports.InteractiveSearchInput, key string) (*ports.InteractiveSearchHeader, error) {
	if err := authorizeInteractiveSearch(actor); err != nil {
		return nil, err
	}
	// Reject controls even in surrounding whitespace before Unicode trimming.
	if !utf8.ValidString(input.Query) || strings.ContainsFunc(input.Query, unicode.IsControl) {
		return nil, ports.ErrRejected
	}
	input.Query = strings.TrimSpace(input.Query)
	if !validInteractiveSearchKey(key) || len(input.Query) > 2048 || utf8.RuneCountInString(input.Query) < 1 || utf8.RuneCountInString(input.Query) > 256 ||
		(input.MediaID != nil && *input.MediaID == uuid.Nil) || (input.SubscriptionID != nil && *input.SubscriptionID == uuid.Nil) {
		return nil, ports.ErrRejected
	}
	input.MediaID = cloneInteractiveValue(input.MediaID)
	input.SubscriptionID = cloneInteractiveValue(input.SubscriptionID)
	actor = cloneInteractiveActor(actor)
	if s == nil || s.repo == nil || s.secrets == nil {
		return nil, ports.ErrUnavailable
	}
	hash, err := s.interactiveSearchHash("create", uuid.Nil, input)
	if err != nil {
		return nil, err
	}
	header, err := s.repo.CreateInteractiveSearch(ctx, actor, ports.PreparedInteractiveSearch{InteractiveSearchInput: input, RankingPolicy: quality.DefaultPolicy()}, key, hash)
	return interactiveSearchHeaderResult(header, err)
}

func (s *InteractiveSearchService) Read(ctx context.Context, actor *ports.Actor, id uuid.UUID) (*ports.InteractiveSearchSnapshot, error) {
	if err := authorizeInteractiveSearch(actor); err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	snapshot, err := s.repo.ReadInteractiveSearch(ctx, cloneInteractiveActor(actor), id)
	if err != nil {
		return nil, safeInteractiveSearchError(err)
	}
	if snapshot == nil {
		return nil, ports.ErrUnavailable
	}
	out := *snapshot
	out.Header = cloneInteractiveSearchHeader(snapshot.Header)
	out.Sources = slices.Clone(snapshot.Sources)
	for i := range out.Sources {
		out.Sources[i].QueryIDs = slices.Clone(out.Sources[i].QueryIDs)
	}
	out.Candidates, err = RankInteractiveSearchCandidates(snapshot.Candidates, out.Header.RankingPolicy)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	out.TotalCandidates = len(out.Candidates)
	out.Truncated = out.TotalCandidates > 500
	if out.Truncated {
		out.Candidates = slices.Clone(out.Candidates[:500])
		out.Partial = true
	}
	out.StatisticsMode = "latest_persisted"
	// Preserve persisted running/failed/complete and source states, including
	// empty snapshots. Absence of candidates alone never means no match.
	return &out, nil
}

func (s *InteractiveSearchService) Cancel(ctx context.Context, actor *ports.Actor, id uuid.UUID, key string) (*ports.InteractiveSearchHeader, error) {
	if err := authorizeInteractiveSearch(actor); err != nil {
		return nil, err
	}
	if id == uuid.Nil || !validInteractiveSearchKey(key) {
		return nil, ports.ErrRejected
	}
	actor = cloneInteractiveActor(actor)
	if s == nil || s.repo == nil || s.secrets == nil {
		return nil, ports.ErrUnavailable
	}
	hash, err := s.interactiveSearchHash("cancel", id, ports.InteractiveSearchInput{})
	if err != nil {
		return nil, err
	}
	header, err := s.repo.CancelInteractiveSearch(ctx, actor, id, key, hash)
	return interactiveSearchHeaderResult(header, err)
}

func (s *InteractiveSearchService) Receipt(ctx context.Context, actor *ports.Actor, key, action string) (*ports.InteractiveSearchHeader, error) {
	if err := authorizeInteractiveSearch(actor); err != nil {
		return nil, err
	}
	if !validInteractiveSearchKey(key) || (action != "create" && action != "cancel") {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	header, err := s.repo.ReadInteractiveSearchReceipt(ctx, cloneInteractiveActor(actor), key, action)
	return interactiveSearchHeaderResult(header, err)
}

func authorizeInteractiveSearch(actor *ports.Actor) error {
	if actor == nil {
		return ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.Scopes != nil || (actor.Role != ports.RoleMember && actor.Role != ports.RoleAdmin) {
		return ports.ErrForbidden
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil {
		return ports.ErrUnauthorized
	}
	return nil
}

func validInteractiveSearchKey(key string) bool {
	if len(key) < 8 || len(key) > 130 {
		return false
	}
	for _, c := range key {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

func (s *InteractiveSearchService) interactiveSearchHash(action string, id uuid.UUID, input ports.InteractiveSearchInput) (string, error) {
	// Hash only semantic command input; the repo freezes the selected policy on
	// first creation. Retries remain stable across later display policy changes.
	canonical, err := json.Marshal(struct {
		Contract       string
		Action         string
		SearchID       uuid.UUID
		Query          string
		MediaID        *uuid.UUID
		SubscriptionID *uuid.UUID
	}{"interactive-search.v1", action, id, input.Query, input.MediaID, input.SubscriptionID})
	if err != nil {
		return "", ports.ErrUnavailable
	}
	hash, err := s.secrets.DigestSecret(canonical)
	if err != nil || hash == "" {
		return "", ports.ErrUnavailable
	}
	return hash, nil
}

func safeInteractiveSearchError(err error) error {
	for _, safe := range []error{ports.ErrInteractiveSearchConflict, ports.ErrCommandConflict, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrUserDisabled, ports.ErrNotFound, ports.ErrRejected, ports.ErrUnavailable, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}

func interactiveSearchHeaderResult(header *ports.InteractiveSearchHeader, err error) (*ports.InteractiveSearchHeader, error) {
	if err != nil {
		return nil, safeInteractiveSearchError(err)
	}
	if header == nil {
		return nil, ports.ErrUnavailable
	}
	out := cloneInteractiveSearchHeader(*header)
	return &out, nil
}

func cloneInteractiveValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneInteractiveActor(actor *ports.Actor) *ports.Actor {
	out := *actor
	out.Scopes = slices.Clone(actor.Scopes)
	return &out
}

func cloneInteractiveSearchHeader(header ports.InteractiveSearchHeader) ports.InteractiveSearchHeader {
	header.MediaID = cloneInteractiveValue(header.MediaID)
	header.SubscriptionID = cloneInteractiveValue(header.SubscriptionID)
	header.CancelledAt = cloneInteractiveValue(header.CancelledAt)
	header.RankingPolicy.ResolutionOrder = maps.Clone(header.RankingPolicy.ResolutionOrder)
	header.RankingPolicy.SourceOrder = maps.Clone(header.RankingPolicy.SourceOrder)
	header.RankingPolicy.CustomWords = slices.Clone(header.RankingPolicy.CustomWords)
	return header
}
