package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"sort"
)

type FileMappingService struct{ repo ports.FileMappingRepository }

func NewFileMappingService(repo ports.FileMappingRepository) *FileMappingService {
	return &FileMappingService{repo}
}
func (s *FileMappingService) Options(ctx context.Context, a *ports.Actor) (*ports.FileMappingOptions, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	return s.repo.ReadFileMappingOptions(ctx, a)
}
func (s *FileMappingService) Preview(ctx context.Context, a *ports.Actor, file, media uuid.UUID) (*ports.FileMappingPreview, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if file == uuid.Nil || media == uuid.Nil {
		return nil, ports.ErrInvalidFileMapping
	}
	return s.repo.ReadFileMappingPreview(ctx, a, file, media)
}
func (s *FileMappingService) Correct(ctx context.Context, a *ports.Actor, file uuid.UUID, c ports.FileMappingCommand, key string) (*ports.FileMappingReceipt, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	if file == uuid.Nil || c.MediaID == uuid.Nil || !c.ConfirmedCoverage || len(c.UnitIDs) == 0 || len(c.UnitIDs) > 20 || len(c.ExpectedStamp) != 64 || key == "" || len(key) > 160 {
		return nil, ports.ErrInvalidFileMapping
	}
	if _, e := hex.DecodeString(c.ExpectedStamp); e != nil {
		return nil, ports.ErrInvalidFileMapping
	}
	c.UnitIDs = append([]uuid.UUID(nil), c.UnitIDs...)
	sort.Slice(c.UnitIDs, func(i, j int) bool { return c.UnitIDs[i].String() < c.UnitIDs[j].String() })
	for i, id := range c.UnitIDs {
		if id == uuid.Nil || i > 0 && id == c.UnitIDs[i-1] {
			return nil, ports.ErrInvalidFileMapping
		}
	}
	raw, _ := json.Marshal([]any{"file.mapping.correct", file, c})
	h := sha256.Sum256(raw)
	return s.repo.CorrectFileMapping(ctx, a, file, c, key, hex.EncodeToString(h[:]))
}
