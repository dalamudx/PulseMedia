package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type companionExecutionRow struct {
	Candidate      ports.CompanionCandidate `json:"candidate"`
	Action         string                   `json:"action"`
	ExecutionToken int64                    `json:"execution_token"`
	Content        *ports.TransferContent   `json:"content"`
}

func companionExecutionValues(e ports.CompanionExecution, state string) map[string]any {
	return map[string]any{"operation_id": e.Attachment.OperationID, "id": e.Attachment.ID, "owner": e.Owner, "token": e.Token, "state": state}
}
func companionExecutionDocument(e ports.CompanionExecution, state string) []byte {
	return reviewJSON(companionExecutionValues(e, state))
}
func companionExecutionCurrent(ctx context.Context, tx pgx.Tx, e ports.CompanionExecution, state string) (*companionExecutionRow, error) {
	raw, err := db.New(tx).GuardUntrackedCompanionExecution(ctx, companionExecutionDocument(e, state))
	if err != nil {
		return nil, ports.ErrTransferExecutionRejected
	}
	var row companionExecutionRow
	if json.Unmarshal(raw, &row) != nil || row.Candidate != e.Attachment.Candidate || row.Action != e.Attachment.Action {
		return nil, ports.ErrTransferExecutionRejected
	}
	return &row, nil
}
func (s *UntrackedCompanionStore) executionCommit(ctx context.Context, tx pgx.Tx, a *ports.Actor, e ports.CompanionExecution, state string) error {
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return err
	}
	if _, err := s.guard(ctx, tx, a, e.Attachment.OperationID, true); err != nil {
		return err
	}
	if _, err := companionExecutionCurrent(ctx, tx, e, state); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *UntrackedCompanionStore) ValidateCompanion(ctx context.Context, a *ports.Actor, e ports.CompanionExecution) error {
	tx, _, err := s.begin(ctx, a, e.Attachment.OperationID, true)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	return s.executionCommit(ctx, tx, a, e, "running")
}
func (s *UntrackedCompanionStore) companionAudit(ctx context.Context, tx pgx.Tx, a *ports.Actor, id uuid.UUID, action string, payload any) error {
	q := db.New(tx)
	raw := reviewJSON(payload)
	if _, err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.parent.ids.NewUUID(), ActorType: "user", ActorID: a.ID, Action: action, TargetType: "untracked_companion", TargetID: id.String(), Payload: raw}); err != nil {
		return err
	}
	if err := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "untracked_companion", AggregateID: id.String()}); err != nil {
		return err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "untracked_companion", AggregateID: id.String()})
	if err != nil {
		return err
	}
	_, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.parent.ids.NewUUID(), AggregateType: "untracked_companion", AggregateID: id.String(), EventType: action, Seq: int64(seq), Payload: raw})
	return err
}
