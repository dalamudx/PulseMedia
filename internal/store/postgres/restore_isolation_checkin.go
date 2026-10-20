package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

// The caller holds the active restore row lock and commits the per-item evidence
// together with the existing final reconciliation audit.
func reconcileCheckinDecisions(ctx context.Context, q *db.Queries, r RestoreReconciliation) error {
	if len(r.CheckinDecisions) > 1000 {
		return errors.New("too many check-in reconciliation decisions")
	}
	seen := make(map[uuid.UUID]struct{}, len(r.CheckinDecisions))
	for _, decision := range r.CheckinDecisions {
		if decision.DayID == uuid.Nil || strings.TrimSpace(decision.Evidence) == "" || len(decision.Evidence) > 4096 {
			return errors.New("daily identity and bounded check-in evidence required")
		}
		if _, exists := seen[decision.DayID]; exists {
			return errors.New("duplicate check-in reconciliation decision")
		}
		seen[decision.DayID] = struct{}{}
	}
	for _, decision := range r.CheckinDecisions {
		day, err := q.GetSiteCheckinRestoreDay(ctx, decision.DayID)
		if err != nil {
			return errors.New("check-in reconciliation day unavailable")
		}
		if day.RestoreID.Valid {
			if uuid.UUID(day.RestoreID.Bytes) != r.RestoreID || !day.RestoreOperator.Valid || day.RestoreOperator.String != r.Operator || !day.RestoreEvidence.Valid || day.RestoreEvidence.String != decision.Evidence || !day.RestoreDecision.Valid || day.RestoreDecision.String != "retain_review" {
				return errors.New("check-in reconciliation history conflicts")
			}
			continue
		}
		n, err := q.MarkSiteCheckinRestoreDecision(ctx, db.MarkSiteCheckinRestoreDecisionParams{RestoreID: r.RestoreID, Operator: r.Operator, Evidence: decision.Evidence, DayID: decision.DayID})
		if err != nil || n != 1 {
			return errors.New("check-in reconciliation rejected; resolve daily jobs before release")
		}
	}
	return nil
}
