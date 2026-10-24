package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestUntrackedOrganizationSemanticTimeAndDetachedReceipt(t *testing.T) {
	utc := time.Date(2026, 10, 21, 1, 2, 3, 456000000, time.UTC)
	candidate := &ports.UntrackedReviewCandidate{Units: []ports.UntrackedReviewUnit{{UnitKey: "S01E02", AirDate: &utc}, {UnitKey: "S01E07"}}}
	m := ports.UntrackedOrganizationMaterial{
		Operation:    ports.UntrackedOrganizationOperation{ID: uuid.New(), CreatedAt: utc},
		Confirmation: ports.UntrackedConfirmationReceipt{Material: ports.UntrackedConfirmationMaterial{CreatedAt: utc, Scope: ports.UntrackedScope{ClientVersion: utc, CreatedAt: utc, UpdatedAt: utc}, File: ports.UntrackedDiscoveredFile{FirstObservedAt: utc, LastObservedAt: utc, Observations: 2}, Review: ports.UntrackedReview{Request: ports.UntrackedReviewRequest{CreatedAt: utc}, RecordedAt: &utc, Evidence: &ports.UntrackedReviewEvidence{Candidate: candidate}}}},
	}
	original, _ := json.Marshal(m)
	clone := reviewClone(m)
	zone := time.FixedZone("+08", 8*3600)
	clone.Operation.CreatedAt = utc.In(zone)
	c := &clone.Confirmation.Material
	c.CreatedAt = utc.In(zone)
	c.Scope.CreatedAt = utc.In(zone)
	c.Scope.UpdatedAt = utc.In(zone)
	c.Scope.ClientVersion = utc.In(zone)
	c.Review.Request.CreatedAt = utc.In(zone)
	recorded := utc.In(zone)
	c.Review.RecordedAt = &recorded
	air := utc.In(zone)
	c.Review.Evidence.Candidate.Units[0].AirDate = &air
	c.File.Observations = 99
	c.File.LastObservedAt = utc.Add(time.Hour)
	if organizationMaterialHash(m) != organizationMaterialHash(clone) {
		t.Fatal("equivalent instants or positive telemetry changed semantic hash")
	}
	after, _ := json.Marshal(m)
	if string(after) != string(original) {
		t.Fatal("hash mutated original receipt")
	}
	capture := &untrackedOrganizationCapture{material: m}
	returned := capture.UntrackedOrganizationMaterial()
	returned.Confirmation.Material.Review.Evidence.Candidate.Units[0].UnitKey = "changed"
	if capture.material.Confirmation.Material.Review.Evidence.Candidate.Units[0].UnitKey != "S01E02" {
		t.Fatal("capture exposed nested pointer")
	}
}

func TestUntrackedOrganizationPrivateCaptureTamper(t *testing.T) {
	store := &UntrackedOrganizationStore{}
	capture := &untrackedOrganizationCapture{store: store, key: "key", inputHash: "input", metadata: []byte("original")}
	capture.seal = organizationCaptureSeal(capture)
	capture.metadata[0] = 'X'
	if _, err := store.CommitUntrackedOrganization(context.Background(), capture); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("private metadata tamper accepted", err)
	}
	capture.metadata = []byte("original")
	capture.seal = organizationCaptureSeal(capture)
	capture.material.Operation.ID = uuid.New()
	if _, err := store.CommitUntrackedOrganization(context.Background(), capture); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("private material tamper accepted", err)
	}
	if _, err := store.CommitUntrackedOrganization(context.Background(), nil); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("nil capture accepted", err)
	}
}

func TestUntrackedOrganizationPayloadCannotCarryReplacementIntent(t *testing.T) {
	id := uuid.New()
	good := reviewJSON(map[string]any{"operation_id": id})
	if got, err := organizationPayload(good); err != nil || got != id {
		t.Fatal(got, err)
	}
	for _, raw := range [][]byte{reviewJSON(map[string]any{"operation_id": id, "target_rel": "other.mkv"}), reviewJSON(map[string]any{"operation_id": id, "action": "move"}), []byte(`{}`), []byte(`{"operation_id":null}`)} {
		if _, err := organizationPayload(raw); err == nil {
			t.Fatal("replacement intent accepted", string(raw))
		}
	}
}
