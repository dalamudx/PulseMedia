package search

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// IndexerManagementService performs local preparation only. The repository is
// the authority for current account permissions, versions and command receipts.
type IndexerManagementService struct {
	repo    ports.IndexerManagementRepository
	secrets ports.SecretProtector
}

func NewIndexerManagementService(repo ports.IndexerManagementRepository, secrets ports.SecretProtector) *IndexerManagementService {
	return &IndexerManagementService{repo: repo, secrets: secrets}
}

func (s *IndexerManagementService) Create(ctx context.Context, actor *ports.Actor, input ports.IndexerManagementInput, key string) (*ports.IndexerManagementReceipt, error) {
	return s.configure(ctx, actor, ports.IndexerCreate, uuid.Nil, input, key)
}

func (s *IndexerManagementService) Update(ctx context.Context, actor *ports.Actor, id uuid.UUID, input ports.IndexerManagementInput, key string) (*ports.IndexerManagementReceipt, error) {
	return s.configure(ctx, actor, ports.IndexerUpdate, id, input, key)
}

func (s *IndexerManagementService) configure(ctx context.Context, actor *ports.Actor, action ports.IndexerMutationAction, id uuid.UUID, input ports.IndexerManagementInput, key string) (*ports.IndexerManagementReceipt, error) {
	if err := authorizeIndexerManagement(actor); err != nil {
		return nil, err
	}
	if !validIndexerManagementKey(key) || !validIndexerManagementName(input.Name) || input.Categories == nil {
		return nil, ports.ErrRejected
	}
	if action == ports.IndexerCreate {
		if input.ExpectedVersion != 0 || input.URL == nil {
			return nil, ports.ErrRejected
		}
	} else if id == uuid.Nil || input.ExpectedVersion < 1 {
		return nil, ports.ErrRejected
	}
	// Freeze caller-owned values before invoking the injected codec or repository.
	input = copyIndexerManagementInput(input)
	preserveURL := action == ports.IndexerUpdate && input.URL == nil
	preserveCredential := action == ports.IndexerUpdate && input.Credential == nil
	configurationInput := ports.IndexerConfigurationInput{Protocol: input.Protocol, Categories: input.Categories}
	if input.URL != nil {
		configurationInput.URL = *input.URL
	} else {
		// Reuse the complete URL validator without interpreting or reconstructing a
		// stored secret. All placeholder output is removed before repository entry.
		configurationInput.URL = "https://indexer.invalid"
	}
	if input.Credential != nil {
		configurationInput.Credential = *input.Credential
	}
	if s == nil || s.repo == nil || s.secrets == nil {
		return nil, ports.ErrUnavailable
	}
	configuration, err := PrepareIndexerConfiguration(configurationInput, s.secrets)
	if err != nil {
		return nil, err
	}
	if preserveURL {
		configuration.URLCiphertext = nil
		configuration.DisplayURL = ""
	}
	if preserveCredential {
		configuration.CredentialCiphertext = nil
	}
	hash, err := s.requestHash(action, id, input)
	if err != nil {
		return nil, err
	}
	mutation := ports.IndexerMutation{Action: action, IndexerID: id, Name: input.Name, ExpectedVersion: input.ExpectedVersion, Configuration: configuration, PreserveURL: preserveURL, PreserveCredential: preserveCredential}
	receipt, err := s.repo.MutateIndexer(ctx, actor, mutation, key, hash)
	if err != nil {
		return nil, safeIndexerManagementError(err)
	}
	if receipt == nil {
		return nil, ports.ErrUnavailable
	}
	return receipt, nil
}

func (s *IndexerManagementService) Delete(ctx context.Context, actor *ports.Actor, id uuid.UUID, expectedVersion int64, key string) (*ports.IndexerManagementReceipt, error) {
	if err := authorizeIndexerManagement(actor); err != nil {
		return nil, err
	}
	if id == uuid.Nil || expectedVersion < 1 || !validIndexerManagementKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil || s.secrets == nil {
		return nil, ports.ErrUnavailable
	}
	hash, err := s.requestHash(ports.IndexerDelete, id, ports.IndexerManagementInput{ExpectedVersion: expectedVersion})
	if err != nil {
		return nil, err
	}
	receipt, err := s.repo.MutateIndexer(ctx, actor, ports.IndexerMutation{Action: ports.IndexerDelete, IndexerID: id, ExpectedVersion: expectedVersion}, key, hash)
	if err != nil {
		return nil, safeIndexerManagementError(err)
	}
	if receipt == nil {
		return nil, ports.ErrUnavailable
	}
	return receipt, nil
}

func (s *IndexerManagementService) List(ctx context.Context, actor *ports.Actor) ([]ports.IndexerManagementView, error) {
	if err := authorizeIndexerManagement(actor); err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	views, err := s.repo.ListManagedIndexers(ctx, actor)
	if err != nil {
		return nil, safeIndexerManagementError(err)
	}
	if views == nil {
		views = []ports.IndexerManagementView{}
	}
	return views, nil
}

// This matches command.AuthorizeSession without importing another usecase
// mechanism. A nonnil empty Scopes slice still identifies a token caller.
func authorizeIndexerManagement(actor *ports.Actor) error {
	if actor == nil {
		return ports.ErrUnauthorized
	}
	if actor.Type != ports.ActorTypeUser || actor.Scopes != nil || actor.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil || id == uuid.Nil {
		return ports.ErrUnauthorized
	}
	return nil
}

func validIndexerManagementKey(key string) bool {
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

func validIndexerManagementName(name string) bool {
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 256 || strings.TrimSpace(name) != name {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) })
}

func copyIndexerManagementInput(input ports.IndexerManagementInput) ports.IndexerManagementInput {
	input.Categories = slices.Clone(input.Categories)
	if input.URL != nil {
		value := *input.URL
		input.URL = &value
	}
	if input.Credential != nil {
		value := *input.Credential
		input.Credential = &value
	}
	return input
}

func (s *IndexerManagementService) requestHash(action ports.IndexerMutationAction, id uuid.UUID, input ports.IndexerManagementInput) (string, error) {
	// Dedicated fields intentionally include secrets in the keyed digest only;
	// marshaling IndexerManagementInput would omit its json:"-" fields.
	canonical, err := json.Marshal(struct {
		Contract        string
		Action          ports.IndexerMutationAction
		Target          uuid.UUID
		ExpectedVersion int64
		Name            string
		Protocol        ports.IndexerProtocol
		URL             *string
		Credential      *string
		Categories      []int
	}{"indexer-management.v1", action, id, input.ExpectedVersion, input.Name, input.Protocol, input.URL, input.Credential, input.Categories})
	if err != nil {
		return "", ports.ErrUnavailable
	}
	hash, err := s.secrets.DigestSecret(canonical)
	if err != nil || hash == "" {
		return "", ports.ErrUnavailable
	}
	return hash, nil
}

func safeIndexerManagementError(err error) error {
	for _, safe := range []error{ports.ErrIndexerCommandConflict, ports.ErrCommandConflict, ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrUserDisabled, ports.ErrNotFound, ports.ErrRejected, ports.ErrUnavailable, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}
