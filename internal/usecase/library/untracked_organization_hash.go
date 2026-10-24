package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// This is a new operation seal; the original confirmation keeps its own digest.
func untrackedOrganizationMaterialHash(m ports.UntrackedOrganizationMaterial) string {
	m = untrackedOrganizationMaterialCopy(m)
	m.Operation.CreatedAt = untrackedReviewInstant(m.Operation.CreatedAt)
	m.Operation.MaterialHash = ""
	m.Confirmation.Material = untrackedReviewMaterialNormalized(m.Confirmation.Material)
	file := &m.Confirmation.Material.File
	file.FirstObservedAt, file.LastObservedAt, file.Observations = time.Time{}, time.Time{}, 0
	raw, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
