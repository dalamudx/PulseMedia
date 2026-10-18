package audit

import (
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type AuditEntry = ports.AuditEntry

type Service struct {
	writer ports.AuditWriter
	idgen  ports.IDGen
	clock  ports.Clock
}

func NewService(writer ports.AuditWriter, idgen ports.IDGen, clock ports.Clock) *Service {
	return &Service{writer: writer, idgen: idgen, clock: clock}
}

// Record persists a critical action into the audit_log table.
func (s *Service) Record(ctx context.Context, actor *ports.Actor, action, targetType, targetID string, details map[string]any) (*AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if details == nil {
		details = make(map[string]any)
	}
	entry := AuditEntry{ID: s.idgen.NewUUID(), ActorType: "system", ActorID: "system", Action: action, TargetType: targetType, TargetID: targetID, Details: details, CreatedAt: s.clock.Now()}
	if actor != nil {
		entry.ActorType, entry.ActorID = string(actor.Type), actor.ID
	}
	if err := s.writer.WriteAudit(ctx, entry); err != nil {
		return nil, err
	}
	return &entry, nil
}
