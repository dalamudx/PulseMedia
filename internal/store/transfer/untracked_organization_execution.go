package transfer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"reflect"
	"strings"
	"time"
)

// External operations reuse the native transfer capabilities, not download authority.
type UntrackedOrganizationExecutor struct{ service *Service }

var _ ports.UntrackedOrganizationExecutionRepository = (*UntrackedOrganizationExecutor)(nil)

func NewUntrackedOrganizationExecutor(pool *pgxpool.Pool, ids ports.IDGen, events *outbox.Outbox, files ports.TransferFilesystem) *UntrackedOrganizationExecutor {
	return &UntrackedOrganizationExecutor{service: NewService(pool, ids, events, files)}
}

func NewUntrackedOrganizationExecutorWithMinimumMediaBytes(pool *pgxpool.Pool, ids ports.IDGen, events *outbox.Outbox, files ports.TransferFilesystem, minimum int64) *UntrackedOrganizationExecutor {
	e := NewUntrackedOrganizationExecutor(pool, ids, events, files)
	e.service.minimumMediaBytes = minimum
	return e
}

type externalTransferPermit struct {
	OperationID    uuid.UUID `json:"operation_id"`
	JobID          uuid.UUID `json:"job_id"`
	JobOwner       string    `json:"job_owner"`
	JobToken       int64     `json:"job_token"`
	ExecutionOwner string    `json:"execution_owner"`
	ExecutionToken int64     `json:"execution_token"`
	ContentSize    int64     `json:"content_size"`
	ContentSHA256  string    `json:"content_sha256"`
	ProofHash      string    `json:"proof_hash"`
	IssuedAt       time.Time `json:"issued_at"`
}

func rollbackExternalTransfer(ctx context.Context, tx pgx.Tx) {
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(finish)
}

func sameExternalTransfer(a, b *postgres.UntrackedOrganizationLocked) bool {
	if a == nil || b == nil {
		return false
	}
	x, y := a.Operation, b.Operation
	x.CreatedAt = x.CreatedAt.UTC().Round(0)
	y.CreatedAt = y.CreatedAt.UTC().Round(0)
	return x == y && fileActionPlanStamp(a.Plan) == fileActionPlanStamp(b.Plan) && fileActionStepStamp(a.Step) == fileActionStepStamp(b.Step) && reflect.DeepEqual(a.Units, b.Units)
}

func (e *UntrackedOrganizationExecutor) load(ctx context.Context, lease ports.JobLease) (*postgres.UntrackedOrganizationLocked, error) {
	if e == nil || e.service == nil || e.service.pool == nil || e.service.idgen == nil || e.service.outbox == nil || e.service.files == nil || lease.ID == uuid.Nil || lease.Owner == "" || lease.Token < 1 {
		return nil, ErrExecutionRejected
	}
	operationID, err := e.service.queries.ExternalTransferOperationForJob(ctx, lease.ID)
	if err != nil {
		return nil, err
	}
	tx, err := e.service.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollbackExternalTransfer(ctx, tx)
	record, err := postgres.GuardUntrackedOrganization(ctx, tx, operationID, lease, false)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return record, nil
}

func (e *UntrackedOrganizationExecutor) write(ctx context.Context, record *postgres.UntrackedOrganizationLocked, lease ports.JobLease, terminal bool, work func(*db.Queries, *postgres.UntrackedOrganizationLocked) error) error {
	tx, err := e.service.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackExternalTransfer(ctx, tx)
	fresh, err := postgres.GuardUntrackedOrganization(ctx, tx, record.Operation.ID, lease, false)
	if err != nil {
		return err
	}
	if !sameExternalTransfer(record, fresh) {
		return ErrReviewRequired
	}
	if err = work(db.New(tx), fresh); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return err
	}
	fresh, err = postgres.GuardUntrackedOrganization(ctx, tx, record.Operation.ID, lease, terminal)
	if err != nil {
		return err
	}
	if !sameExternalTransfer(record, fresh) {
		return ErrReviewRequired
	}
	return tx.Commit(ctx)
}

func (e *UntrackedOrganizationExecutor) permit(ctx context.Context, record *postgres.UntrackedOrganizationLocked) (*externalTransferPermit, *ports.TransferContent, error) {
	raw, err := e.service.queries.ExternalTransferPermitRow(ctx, record.Operation.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var p externalTransferPermit
	if json.Unmarshal(raw, &p) != nil || p.OperationID != record.Operation.ID || p.JobID != record.Operation.JobID || p.JobOwner == "" || p.JobToken < 1 || p.ExecutionOwner == "" || p.ExecutionToken < 1 || p.ExecutionOwner != record.Plan.ExecutionOwner.String || p.ExecutionToken != record.Plan.ExecutionToken || p.ContentSize != record.Step.SourceSize || len(p.ProofHash) != 64 || p.IssuedAt.IsZero() {
		return nil, nil, ErrReviewRequired
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(p.ContentSHA256, "\\x"))
	if err != nil || len(digest) != 32 {
		return nil, nil, ErrReviewRequired
	}
	raw, err = e.service.queries.GetVerifiedTransferContent(ctx, record.Step.ID)
	if err != nil {
		return nil, nil, err
	}
	var proof ports.TransferContent
	if json.Unmarshal(raw, &proof) != nil || proof.File != record.Confirmation.Material.File.Identity || proof.File != frozen(record.Step) || !reflect.DeepEqual(digest, proof.SHA256[:]) {
		return nil, nil, ErrReviewRequired
	}
	original := ports.JobLease{ID: p.JobID, Owner: p.JobOwner, Token: p.JobToken}
	expected := recoveryHash([]any{record.Operation, record.Units, fileActionPlanStamp(record.Plan), fileActionStepStamp(record.Step), original, proof})
	if p.ProofHash != expected {
		return nil, nil, ErrReviewRequired
	}
	return &p, &proof, nil
}

func (e *UntrackedOrganizationExecutor) claim(ctx context.Context, record *postgres.UntrackedOrganizationLocked, lease ports.JobLease) (db.TransferPlan, error) {
	var plan db.TransferPlan
	doc, err := json.Marshal(map[string]any{"operation_id": record.Operation.ID, "job_owner": lease.Owner, "job_token": lease.Token, "execution_owner": e.service.idgen.NewUUID().String()})
	if err != nil {
		return plan, err
	}
	err = e.write(ctx, record, lease, false, func(q *db.Queries, _ *postgres.UntrackedOrganizationLocked) error {
		var failure error
		plan, failure = q.ExternalTransferClaimUnpublished(ctx, doc)
		return failure
	})
	return plan, err
}

func (e *UntrackedOrganizationExecutor) renew(ctx context.Context, record *postgres.UntrackedOrganizationLocked, lease ports.JobLease, plan db.TransferPlan) error {
	return e.write(ctx, record, lease, false, func(q *db.Queries, _ *postgres.UntrackedOrganizationLocked) error {
		_, err := q.GuardTransferPlan(ctx, guardParams(plan))
		return err
	})
}

func (e *UntrackedOrganizationExecutor) seal(ctx context.Context, record *postgres.UntrackedOrganizationLocked, lease ports.JobLease, plan db.TransferPlan, proof ports.TransferContent) error {
	if proof.File != record.Confirmation.Material.File.Identity || proof.File != frozen(record.Step) {
		return ErrReviewRequired
	}
	now, err := e.service.queries.GetSearchDatabaseTime(ctx)
	if err != nil || !now.Valid {
		if err != nil {
			return err
		}
		return ErrExecutionRejected
	}
	p := externalTransferPermit{OperationID: record.Operation.ID, JobID: lease.ID, JobOwner: lease.Owner, JobToken: lease.Token, ExecutionOwner: plan.ExecutionOwner.String, ExecutionToken: plan.ExecutionToken, ContentSize: proof.File.Size, ContentSHA256: "\\x" + hex.EncodeToString(proof.SHA256[:]), IssuedAt: now.Time}
	p.ProofHash = recoveryHash([]any{record.Operation, record.Units, fileActionPlanStamp(plan), fileActionStepStamp(record.Step), lease, proof})
	permit, err := json.Marshal(p)
	if err != nil {
		return err
	}
	content, err := json.Marshal(map[string]any{"operation_id": record.Operation.ID, "content": proof})
	if err != nil {
		return err
	}
	return e.write(ctx, record, lease, false, func(q *db.Queries, _ *postgres.UntrackedOrganizationLocked) error {
		if _, failure := q.GuardTransferPlan(ctx, guardParams(plan)); failure != nil {
			return failure
		}
		if _, failure := q.UpdateStepRunning(ctx, record.Step.ID); failure != nil {
			return failure
		}
		rows, failure := q.ExternalTransferSealContent(ctx, content)
		if failure != nil {
			return failure
		}
		if rows != 1 {
			return ErrExecutionRejected
		}
		rows, failure = q.ExternalTransferInsertPermit(ctx, permit)
		if failure != nil {
			return failure
		}
		if rows != 1 {
			return ErrExecutionRejected
		}
		return nil
	})
}

func (e *UntrackedOrganizationExecutor) OrganizeUntrackedFile(ctx context.Context, lease ports.JobLease) error {
	record, err := e.load(ctx, lease)
	if err != nil {
		return err
	}
	cap, ok := e.service.files.(ports.VerifiedTransferFilesystem)
	if !ok {
		return ErrReviewRequired
	}
	m := record.Confirmation.Material
	roots, release, err := e.service.files.LockRoots(ctx, m.Scope.LocalRoot, m.Scope.TargetRootPath)
	if err != nil {
		return err
	}
	defer release()
	fresh, err := e.load(ctx, lease)
	if err != nil {
		return err
	}
	if !sameExternalTransfer(record, fresh) {
		return ErrReviewRequired
	}
	record = fresh
	if err = externalTransferNamespace(ctx, e.service.files, m.Scope); err != nil {
		return err
	}
	permit, proof, err := e.permit(ctx, record)
	if err != nil {
		return err
	}
	if permit != nil {
		result, failure := observeExternalTransfer(ctx, cap, roots, m, *proof, record.Step.ID)
		if failure != nil {
			return failure
		}
		return e.finalize(ctx, record, lease, result)
	}
	if eligible, _ := naming.IsEligibleMediaFile(m.File.RelativePath, m.File.Identity.Size, e.service.minimumMediaBytes); !eligible {
		return ErrReviewRequired
	}
	plan, err := e.claim(ctx, record, lease)
	if err != nil {
		return err
	}
	record.Plan = plan
	return withTransferLease(ctx, func(call context.Context) error { return e.renew(call, record, lease, plan) }, func(call context.Context) error {
		source, target := roots[m.Scope.LocalRoot], roots[m.Scope.TargetRootPath]
		if source == nil || target == nil {
			return ErrReviewRequired
		}
		for _, item := range []struct {
			root     ports.TransferRoot
			identity ports.FileIdentity
		}{{source, m.Scope.SourceIdentity}, {target, m.Scope.TargetIdentity}} {
			identity, failure := item.root.Identity(call)
			if failure != nil {
				return failure
			}
			if !externalTransferIdentity(identity, item.identity) {
				return ErrReviewRequired
			}
		}
		if _, failure := target.Inspect(call, m.Input.TargetRelativePath); failure == nil {
			return ErrReviewRequired
		} else if !errors.Is(failure, os.ErrNotExist) {
			return failure
		}
		content, failure := cap.InspectContent(call, source, m.File.RelativePath, m.File.Identity)
		if failure != nil {
			return failure
		}
		if m.Input.Action == "reflink" {
			supported, probeErr := cap.ProbeReflink(call, source, m.File.RelativePath, target, m.File.Identity, record.Step.ID)
			if probeErr != nil {
				return probeErr
			}
			if !supported {
				return ports.ErrFileUnsupported
			}
		}
		if failure = externalTransferNamespace(call, e.service.files, m.Scope); failure != nil {
			return failure
		}
		if failure = e.seal(call, record, lease, plan, content); failure != nil {
			return failure
		}
		if _, failure = e.load(call, lease); failure != nil {
			return failure
		}
		if failure = externalTransferNamespace(call, e.service.files, m.Scope); failure != nil {
			return failure
		}
		if m.Input.Action == "hardlink" {
			failure = e.service.files.Hardlink(call, source, m.File.RelativePath, target, m.Input.TargetRelativePath, content.File)
		} else {
			_, failure = cap.TransferVerified(call, source, m.File.RelativePath, target, m.Input.TargetRelativePath, content, m.Input.Action, record.Step.ID)
		}
		if failure != nil {
			return failure
		}
		observed, failure := observeExternalTransfer(call, cap, roots, m, content, record.Step.ID)
		if failure != nil {
			return failure
		}
		return e.finalize(call, record, lease, observed)
	}, 20*time.Second)
}

func (e *UntrackedOrganizationExecutor) finalize(ctx context.Context, record *postgres.UntrackedOrganizationLocked, lease ports.JobLease, observed ports.VerifiedTransferResult) error {
	permit, proof, err := e.permit(ctx, record)
	if err != nil {
		return err
	}
	if permit == nil || proof == nil || observed.Target.SHA256 != proof.SHA256 || observed.Target.File.Size != proof.File.Size || observed.SourceRemoved != (record.Step.Action == "move") {
		return ErrReviewRequired
	}
	now, err := e.service.queries.GetSearchDatabaseTime(ctx)
	if err != nil {
		return err
	}
	if !now.Valid {
		return ErrExecutionRejected
	}
	observationID := e.service.idgen.NewUUID()
	observation, err := json.Marshal(map[string]any{"id": observationID, "operation_id": record.Operation.ID, "job_id": lease.ID, "job_owner": lease.Owner, "job_token": lease.Token, "result": observed, "recorded_at": now.Time})
	if err != nil {
		return err
	}
	return e.write(ctx, record, lease, true, func(q *db.Queries, fresh *postgres.UntrackedOrganizationLocked) error {
		if len(fresh.Units) == 0 {
			return ErrReviewRequired
		}
		if _, failure := q.ExternalTransferInsertObservation(ctx, observation); failure != nil {
			return failure
		}
		root, failure := q.GetLibraryRootByID(ctx, fresh.Plan.RootID)
		if failure != nil {
			return failure
		}
		file, failure := e.service.targetFile(ctx, q, root, fresh.Step, observed.Target.File)
		if failure != nil {
			return failure
		}
		for _, unit := range fresh.Units {
			if failure = q.LinkFileUnit(ctx, db.LinkFileUnitParams{FileID: file.ID, UnitID: unit.UnitID}); failure != nil {
				return failure
			}
			if _, failure = q.CreateUnitFact(ctx, db.CreateUnitFactParams{ID: e.service.idgen.NewUUID(), UnitID: unit.UnitID, Stage: "imported", SourceFileID: pgtype.UUID{Bytes: file.ID, Valid: true}, RootID: pgtype.UUID{Bytes: root.ID, Valid: true}, QualityProfile: fresh.Step.QualityProfile}); failure != nil {
				return failure
			}
		}
		if _, failure = q.IncrementRootMutationSeq(ctx, root.ID); failure != nil {
			return failure
		}
		for _, unit := range fresh.Units {
			payload := map[string]any{"operation_id": record.Operation.ID, "file_id": file.ID, "unit_id": unit.UnitID, "target_path": fresh.Step.TargetPath}
			if _, failure = e.service.outbox.PublishImported(ctx, q, unit.UnitID.String(), payload); failure != nil {
				return failure
			}
		}
		if _, failure = q.CompleteStep(ctx, db.CompleteStepParams{ID: fresh.Step.ID, TargetFileID: pgtype.UUID{Bytes: file.ID, Valid: true}}); failure != nil {
			return failure
		}
		result := map[string]any{"state": "completed", "file_id": file.ID, "operation_id": record.Operation.ID, "step_id": fresh.Step.ID, "observation_id": observationID, "proof_hash": permit.ProofHash}
		completion, marshalErr := json.Marshal(map[string]any{"operation_id": record.Operation.ID, "step_id": fresh.Step.ID, "file_id": file.ID, "observation_id": observationID, "result": observed, "recorded_at": now.Time})
		if marshalErr != nil {
			return marshalErr
		}
		rows, failure := q.ExternalTransferInsertCompletion(ctx, completion)
		if failure != nil {
			return failure
		}
		if rows != 1 {
			return ErrExecutionRejected
		}
		rows, failure = q.ExternalTransferFinishPlan(ctx, record.Operation.ID)
		if failure != nil {
			return failure
		}
		if rows != 1 {
			return ErrExecutionRejected
		}
		audit, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return marshalErr
		}
		if _, failure = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: e.service.idgen.NewUUID(), ActorType: "user", ActorID: record.Operation.ActorID.String(), Action: "untracked.organization.imported", TargetType: "untracked_organization", TargetID: record.Operation.ID.String(), Payload: audit}); failure != nil {
			return failure
		}
		finish, marshalErr := json.Marshal(map[string]any{"operation_id": record.Operation.ID, "job_owner": lease.Owner, "job_token": lease.Token, "result": observed})
		if marshalErr != nil {
			return marshalErr
		}
		rows, failure = q.ExternalTransferFinishJob(ctx, finish)
		if failure != nil {
			return failure
		}
		if rows != 1 {
			return ErrExecutionRejected
		}
		return nil
	})
}

func (e *UntrackedOrganizationExecutor) RecoverUntrackedOrganizations(ctx context.Context, limit int) (int, error) {
	if e == nil || e.service == nil || e.service.pool == nil || limit < 1 || limit > 64 {
		return 0, ports.ErrRejected
	}
	ids, err := e.service.queries.ExternalTransferRecoverJobs(ctx, int32(limit))
	return len(ids), err
}
