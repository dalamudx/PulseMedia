package app

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// A tracking-only wake still needs the native executor. Losing that dependency
// must return a controlled error instead of panicking the process mid-lease.
func TestCompleteTVTrackingWakeRequiresNativeDependencies(t *testing.T) {
	payload, err := json.Marshal(map[string]uuid.UUID{"operation_id": uuid.New(), "task_id": uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	job := &db.Job{ID: uuid.New(), Kind: ports.CompleteTVTrackingJobKind, Payload: payload, LeaseOwner: pgtype.Text{String: "tracking-wake-owner", Valid: true}, LeaseToken: 1}
	if err = handleCompleteTVJob(t.Context(), nil, nil, nil, nil, job); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("missing native dependency must fail closed", err)
	}
}
