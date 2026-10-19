package mediaserver

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"sync"
)

var (
	ErrFileRetired       = ports.ErrMediaFileRetired
	ErrItemNotIndexedYet = errors.New("file not yet indexed by media server")
	ErrServerUnreachable = errors.New("media server unreachable or offline")
)

type ServerFactory func(string, string, string) (ports.MediaServerClient, error)
type SyncService struct {
	outbox        ports.DeliveryFailureWriter
	mu            sync.RWMutex
	clients       map[uuid.UUID]ports.MediaServerClient
	decoder       ports.CredentialDecoder
	factory       ServerFactory
	confirmations ports.MediaConfirmationRepository
}

func NewSyncService(ob ports.DeliveryFailureWriter, confirmations ports.MediaConfirmationRepository) *SyncService {
	return &SyncService{outbox: ob, confirmations: confirmations, clients: make(map[uuid.UUID]ports.MediaServerClient)}
}
func (s *SyncService) RegisterClient(id uuid.UUID, c ports.MediaServerClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[id] = c
}
func (s *SyncService) SetCredentialDecoder(decoder ports.CredentialDecoder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decoder = decoder
}
func (s *SyncService) SetServerFactory(f ServerFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.factory = f
}
func (s *SyncService) clientForConnection(row ports.MediaServerConnection) (ports.MediaServerClient, error) {
	s.mu.RLock()
	client := s.clients[row.ID]
	factory := s.factory
	decoder := s.decoder
	s.mu.RUnlock()
	if client != nil {
		return client, nil
	}
	if factory == nil || decoder == nil {
		return nil, ErrServerUnreachable
	}
	plain, err := decoder.DecryptSecret(row.EncryptedKey)
	if err != nil {
		return nil, ErrServerUnreachable
	}
	client, err = factory(row.Kind, row.URL, string(plain))
	if err != nil || client == nil {
		return nil, ErrServerUnreachable
	}
	return client, nil
}
func (s *SyncService) TestServer(ctx context.Context, id uuid.UUID) (bool, string, error) {
	if s.confirmations == nil {
		return false, "media server unavailable", ports.ErrUnavailable
	}
	row, err := s.confirmations.LoadMediaServerConnection(ctx, id)
	if err != nil {
		if ctx.Err() != nil {
			return false, "media server unavailable", ctx.Err()
		}
		if errors.Is(err, ports.ErrNotFound) {
			return false, "server not found", ports.ErrNotFound
		}
		return false, "media server unavailable", ports.ErrUnavailable
	}
	c, err := s.clientForConnection(row)
	if err != nil || c == nil {
		return false, "media server unavailable", ports.ErrUnavailable
	}
	if ctx.Err() != nil {
		return false, "media server unavailable", ctx.Err()
	}
	if err = c.Test(ctx); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return false, "media server unavailable", ctx.Err()
		}
		return false, "media server unavailable", errors.Join(ErrServerUnreachable, ports.ErrEndpointProbeFailed)
	}
	if ctx.Err() != nil {
		return false, "media server unavailable", ctx.Err()
	}
	return true, "ok", nil
}

func (s *SyncService) ConfirmFile(ctx context.Context, serverID, fileID, unitID uuid.UUID) (*ports.MediaConfirmationFact, error) {
	input, err := s.confirmations.LoadMediaConfirmation(ctx, serverID, fileID, unitID)
	if err != nil {
		return nil, err
	}
	client, err := s.clientForConnection(input.Server)
	if err != nil {
		return nil, err
	}
	item, found, err := client.FindByPath(ctx, input.RemotePath)
	if err != nil {
		return nil, ErrServerUnreachable
	}
	if !found {
		return nil, ErrItemNotIndexedYet
	}
	return s.confirmations.CommitMediaConfirmation(ctx, *input, item)
}
func TranslatePath(mapping map[string]string, path string) string {
	return naming.TranslateRemotePath(mapping, path)
}
