package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type organizationExecutionBasis struct {
	actor                         *ports.Actor
	sub                           uuid.UUID
	command, claim, prepared, job json.RawMessage
	physical                      upgradeOrganizationPhysical
	marker, key                   string
	verify                        func() error
}

func (s *organizationExecution) load(ctx context.Context, tx pgx.Tx) (*organizationExecutionBasis, error) {
	a, sub, e := s.repo.store.authorizePreparation(ctx, tx, s.actor, s.base.operation)
	if e != nil {
		return nil, e
	}
	if e = db.New(tx).LockDownloadAcquisition(ctx, s.base.operation); e != nil {
		return nil, e
	}
	b := &organizationExecutionBasis{actor: a, sub: sub}
	var prep uuid.UUID
	e = tx.QueryRow(ctx, `SELECT to_jsonb(b),preparation_id,request_key FROM movie_upgrade_organization_job b WHERE job_id=$1 AND operation_id=$2 AND actor_id=$3::uuid FOR SHARE`, s.lease.ID, s.base.operation, a.ID).Scan(&b.command, &prep, &b.key)
	if e != nil {
		return nil, e
	}
	logical := movieUpgradeOrganizationJobRepository{store: s.repo.store}
	b.prepared, b.verify, e = logical.basis(ctx, tx, a, s.base.operation, prep)
	if e != nil {
		return nil, e
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT to_jsonb(c),to_jsonb(j),to_jsonb(j)=c.claimed_job AND j.state='running' AND j.lease_owner=$2 AND j.lease_token=$3 AND j.lease_until>clock_timestamp() FROM movie_upgrade_organization_job_claim c JOIN job j ON j.id=c.job_id WHERE j.id=$1 FOR UPDATE OF j FOR SHARE OF c`, s.lease.ID, s.lease.Owner, s.lease.Token).Scan(&b.claim, &b.job, &valid)
	if e != nil {
		return nil, e
	}
	if !valid {
		return nil, preparationConflict()
	}
	var fields struct {
		Snapshot struct {
			Physical upgradeOrganizationPhysical `json:"physical"`
		} `json:"snapshot"`
	}
	if json.Unmarshal(b.prepared, &fields) != nil {
		return nil, preparationConflict()
	}
	b.physical = fields.Snapshot.Physical
	e = tx.QueryRow(ctx, `SELECT marker_filename FROM library_root WHERE id=(SELECT root_id FROM movie_upgrade_authority WHERE operation_id=$1)`, s.base.operation).Scan(&b.marker)
	if e != nil {
		return nil, e
	}
	if b.marker == "" {
		b.marker = ".pulsemedia_root"
	}
	if e = s.check(ctx, tx, b, b.job); e != nil {
		return nil, e
	}
	return b, nil
}
func (s *organizationExecution) claimLedger(ctx context.Context, tx pgx.Tx, b *organizationExecutionBasis) (ownedUpgradeRows, error) {
	var c struct {
		ID uuid.UUID `json:"claim_id"`
		At time.Time `json:"captured_at"`
	}
	var command struct {
		ID   uuid.UUID `json:"command_id"`
		Prep uuid.UUID `json:"preparation_id"`
	}
	if json.Unmarshal(b.claim, &c) != nil || json.Unmarshal(b.command, &command) != nil {
		return nil, preparationConflict()
	}
	receipt := &ports.MovieUpgradeOrganizationJobClaim{ClaimID: c.ID, JobID: s.lease.ID, CommandID: command.ID, OperationID: s.base.operation, PreparationID: command.Prep, ClaimedAt: c.At, Status: "claim_captured_not_executable", ExecutionAllowed: false}
	hash := coverageDigest([]any{"upgrade.organization-claim.v1", s.lease, b.claim})
	return readUpgradeObservationLedger(ctx, tx, s.base.operation, b.actor, b.sub, s.lease.ID.String(), hash, "upgrade.organization-claim", receipt, true)
}
func (s *organizationExecution) check(ctx context.Context, tx pgx.Tx, b *organizationExecutionBasis, expectedJob json.RawMessage) error {
	if e := b.verify(); e != nil {
		return e
	}
	logical := movieUpgradeOrganizationJobRepository{store: s.repo.store}
	receipt, e := logical.recover(ctx, tx, b.actor, b.sub, s.base.operation, strings.TrimPrefix(b.key, "upgrade.organization-queue:"), nil)
	if e != nil {
		return e
	}
	if receipt == nil || receipt.JobID != s.lease.ID {
		return preparationConflict()
	}
	ledger, e := s.claimLedger(ctx, tx, b)
	if e != nil {
		return e
	}
	if e = ledger.verify(ctx, tx); e != nil {
		return e
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT to_jsonb(b)=$2::jsonb AND to_jsonb(c)=$3::jsonb AND to_jsonb(j)=$4::jsonb AND to_jsonb(p)=$5::jsonb AND (c.claimed_job->>'lease_until')::timestamptz>clock_timestamp() FROM movie_upgrade_organization_job b JOIN movie_upgrade_organization_job_claim c USING(job_id) JOIN job j ON j.id=b.job_id JOIN movie_upgrade_organization_preparation p ON p.id=b.preparation_id WHERE b.job_id=$1`, s.lease.ID, b.command, b.claim, expectedJob, b.prepared).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return preparationConflict()
	}
	return nil
}
