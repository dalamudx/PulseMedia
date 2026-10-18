package download

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"testing"
)

func TestTaskDTOConvertsWithoutDiscardingUnknownOutcome(t *testing.T) {
	id, op, plan := uuid.New(), uuid.New(), uuid.New()
	in := &db.DownloadTask{ID: id, OperationID: op, State: "unknown", Progress: 0.5, TotalBytes: 1234, DownloadedBytes: 617, ContentPath: pgtype.Text{String: "/configured/file.mkv", Valid: true}, TransferPlanID: pgtype.UUID{Bytes: plan, Valid: true}, OrganizationState: "review_required"}
	out, e := taskDTO(in, ports.ErrDownloadReconcilePending)
	if out == nil || out.ID != id || out.OperationID != op || out.State != "unknown" || out.Progress != 0.5 || out.TotalBytes != 1234 || out.DownloadedBytes != 617 || out.ContentPath != in.ContentPath.String || out.TransferPlanID == nil || *out.TransferPlanID != plan || !errors.Is(e, ports.ErrDownloadReconcilePending) {
		t.Fatal("partial task evidence lost")
	}
	if out, e = taskDTO(nil, ports.ErrDownloadReconcilePending); out != nil || !errors.Is(e, ports.ErrDownloadReconcilePending) {
		t.Fatal("nil task promoted")
	}
}
