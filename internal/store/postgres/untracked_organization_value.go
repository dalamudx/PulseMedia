package postgres

import (
	"context"
	"errors"
	"path"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type untrackedOrganizationRow struct {
	ID             uuid.UUID                           `json:"id"`
	OwnerID        uuid.UUID                           `json:"owner_id"`
	ScopeID        uuid.UUID                           `json:"scope_id"`
	FileID         uuid.UUID                           `json:"file_id"`
	ConfirmationID uuid.UUID                           `json:"confirmation_id"`
	PlanID         uuid.UUID                           `json:"plan_id"`
	StepID         uuid.UUID                           `json:"step_id"`
	JobID          uuid.UUID                           `json:"job_id"`
	CommandKey     string                              `json:"command_key"`
	InputHash      string                              `json:"input_hash"`
	Material       ports.UntrackedOrganizationMaterial `json:"material"`
	Receipt        ports.UntrackedOrganizationReceipt  `json:"receipt"`
}

type untrackedOrganizationCapture struct {
	store                 *UntrackedOrganizationStore
	actor                 ports.Actor
	material              ports.UntrackedOrganizationMaterial
	key, inputHash, seal  string
	metadata              []byte
	deadline, issuedLocal time.Time
}

func (c *untrackedOrganizationCapture) UntrackedOrganizationMaterial() ports.UntrackedOrganizationMaterial {
	return reviewClone(c.material)
}

// Normalize copies only: the immutable original receipt retains its time offsets.
func organizationCanonical(m ports.UntrackedOrganizationMaterial) ports.UntrackedOrganizationMaterial {
	m = reviewClone(m)
	m.Operation.CreatedAt = m.Operation.CreatedAt.UTC()
	m.Operation.MaterialHash = ""
	c := &m.Confirmation.Material
	c.CreatedAt = c.CreatedAt.UTC()
	c.Scope = reviewScopeCanonical(c.Scope)
	c.File = reviewStableFile(c.File)
	c.Review.Request = reviewRequestCanonical(c.Review.Request)
	if c.Review.RecordedAt != nil {
		d := c.Review.RecordedAt.UTC()
		c.Review.RecordedAt = &d
	}
	if c.Review.Evidence != nil && c.Review.Evidence.Candidate != nil {
		for i := range c.Review.Evidence.Candidate.Units {
			if d := c.Review.Evidence.Candidate.Units[i].AirDate; d != nil {
				t := d.UTC()
				c.Review.Evidence.Candidate.Units[i].AirDate = &t
			}
		}
	}
	return m
}

func organizationMaterialHash(m ports.UntrackedOrganizationMaterial) string {
	return watchDigest(organizationCanonical(m))
}

func organizationMaterialValid(m ports.UntrackedOrganizationMaterial) bool {
	o, c := m.Operation, m.Confirmation.Material
	seen := map[uuid.UUID]bool{}
	for _, id := range []uuid.UUID{o.ID, o.PlanID, o.StepID, o.JobID} {
		if id == uuid.Nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return o.ActorID != uuid.Nil && o.ScopeID != uuid.Nil && o.FileID != uuid.Nil && o.ConfirmationID != uuid.Nil &&
		!o.CreatedAt.IsZero() && o.MaterialHash == organizationMaterialHash(m) &&
		o.ActorID == c.ActorID && o.ScopeID == c.Scope.ID && o.ActorID == c.Scope.OwnerID &&
		o.FileID == c.File.ID && c.File.ScopeID == c.Scope.ID && o.ConfirmationID == c.ID &&
		o.ScopeVersion == c.Scope.Version && o.FileRevision == c.File.Revision &&
		m.Confirmation.State == untrackedConfirmationState && !m.Confirmation.ExecutionAllowed &&
		reviewConfirmationInputValid(c.Input) && c.Input.ScopeVersion == o.ScopeVersion && c.Input.FileRevision == o.FileRevision &&
		c.Scope.Enabled && untrackedScopeValid(c.Scope) && reviewFileValid(c.File, c.Scope) &&
		c.Review.Evidence != nil && reviewEvidenceValid(c.Review.Request.Input, *c.Review.Evidence) &&
		c.Review.Evidence.Complete && reviewText(c.Review.Evidence.ParserVersion, 128, true) &&
		c.Review.Evidence.Candidate != nil && c.Review.Evidence.Candidate.Key == c.Input.CandidateKey
}

func organizationError(ctx context.Context, err error) error {
	if errors.Is(err, ports.ErrUntrackedOrganizationStale) || errors.Is(err, ports.ErrUntrackedReviewStale) || errors.Is(err, ports.ErrUntrackedDiscoveryStale) {
		return ports.ErrUntrackedOrganizationStale
	}
	if errors.Is(err, ports.ErrEndpointVersionConflict) {
		return ports.ErrEndpointVersionConflict
	}
	return reviewError(ctx, err)
}

func organizationDecode(raw []byte) (untrackedOrganizationRow, error) {
	var r untrackedOrganizationRow
	if checkinDecode(raw, &r) != nil {
		return r, ports.ErrUntrackedOrganizationStale
	}
	o := r.Material.Operation
	if !organizationMaterialValid(r.Material) || r.ID != o.ID || r.OwnerID != o.ActorID || r.ScopeID != o.ScopeID || r.FileID != o.FileID ||
		r.ConfirmationID != o.ConfirmationID || r.PlanID != o.PlanID || r.StepID != o.StepID || r.JobID != o.JobID ||
		!doubanSyncASCII(r.CommandKey, 160, false) || r.InputHash != watchDigest(ports.UntrackedOrganizationInput{ConfirmationID: o.ConfirmationID}) ||
		r.Receipt.CommandKey != r.CommandKey || r.Receipt.ExecutionAllowed || r.Receipt.State != "queued" ||
		!reviewJSONSame(reviewJSON(r.Receipt.Operation), reviewJSON(o)) {
		return r, ports.ErrUntrackedOrganizationStale
	}
	return r, nil
}

func organizationConfirmation(ctx context.Context, q *db.Queries, owner, scope, file, id uuid.UUID) (*ports.UntrackedConfirmationReceipt, error) {
	raw, err := q.GetUntrackedOrganizationConfirmation(ctx, id)
	if err != nil {
		return nil, err
	}
	var r untrackedConfirmationRow
	if checkinDecode(raw, &r) != nil {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	if r.OwnerID != owner || r.ScopeID != scope || r.FileID != file {
		return nil, ports.ErrNotFound
	}
	receipt, hash, err := reviewConfirmationKey(ctx, q, owner, scope, file, r.CommandKey)
	if err != nil {
		return nil, err
	}
	if r.ID != id || receipt.Material.ID != id || hash != r.InputHash || !reviewJSONSame(raw, reviewJSON(r)) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	return receipt, nil
}

// Historical validation deliberately does not consult enabled/configuration/FS state.
func organizationOriginal(ctx context.Context, q *db.Queries, r untrackedOrganizationRow, owner, scope, file uuid.UUID) error {
	if r.OwnerID != owner || r.ScopeID != scope || r.FileID != file {
		return ports.ErrNotFound
	}
	c, err := organizationConfirmation(ctx, q, owner, scope, file, r.ConfirmationID)
	if err != nil {
		return err
	}
	if !reviewJSONSame(reviewJSON(c), reviewJSON(r.Material.Confirmation)) {
		return ports.ErrUntrackedOrganizationStale
	}
	return nil
}

func organizationCurrent(ctx context.Context, tx pgx.Tx, m ports.UntrackedOrganizationMaterial) error {
	if !organizationMaterialValid(m) || m.Confirmation.Material.Review.Evidence.ParserVersion != "untracked-review.v1/"+media.EpisodeParserVersion {
		return ports.ErrUntrackedOrganizationStale
	}
	o, c := m.Operation, m.Confirmation.Material
	q := db.New(tx)
	if err := untrackedGuard(ctx, q, discoveryActor(o.ActorID)); err != nil {
		return err
	}
	scope, file, err := reviewOwned(ctx, tx, o.ActorID, o.ScopeID, o.FileID, true)
	if err != nil {
		return err
	}
	if !untrackedSame(*scope, c.Scope) || !reviewFileSame(*file, c.File) {
		return ports.ErrUntrackedOrganizationStale
	}
	if _, err = discoveryMaterial(ctx, q, *scope); err != nil {
		return err
	}
	original, err := organizationConfirmation(ctx, q, o.ActorID, o.ScopeID, o.FileID, o.ConfirmationID)
	if err != nil {
		return err
	}
	if !reviewJSONSame(reviewJSON(original), reviewJSON(m.Confirmation)) {
		return ports.ErrUntrackedOrganizationStale
	}
	// Explicit canonical selection must retain the versions confirmed by the user.
	if err = reviewCanonicalSelection(ctx, tx, c.Review.Request.Input, true); err != nil {
		return err
	}
	return nil
}

func organizationQuality(m ports.UntrackedOrganizationMaterial) []byte {
	return reviewJSON(media.RecognizeRelease(path.Base(m.Confirmation.Material.File.RelativePath)).Quality)
}

func organizationPayload(raw []byte) (uuid.UUID, error) {
	var p struct {
		OperationID uuid.UUID `json:"operation_id"`
	}
	if checkinDecode(raw, &p) != nil || p.OperationID == uuid.Nil || !reviewJSONSame(raw, reviewJSON(p)) {
		return uuid.Nil, ports.ErrUntrackedOrganizationStale
	}
	return p.OperationID, nil
}
