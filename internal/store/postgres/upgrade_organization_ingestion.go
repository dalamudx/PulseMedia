package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const upgradeIngestionAction = "upgrade.organization-ingest"

// MovieUpgradeImportedPublisher joins the existing importing transaction.
// Its implementation freezes each refresh and notification recipient.
type MovieUpgradeImportedPublisher interface {
	PublishImported(context.Context, *db.Queries, string, any) (*db.OutboxEvent, error)
}

type movieUpgradeOrganizationIngestionRepository struct {
	store     *UpgradePreparationStore
	files     ports.TransferFilesystem
	publisher MovieUpgradeImportedPublisher
}

func NewMovieUpgradeOrganizationIngestionRepository(s *UpgradePreparationStore, files ports.TransferFilesystem, publisher MovieUpgradeImportedPublisher) (ports.MovieUpgradeOrganizationIngestionRepository, error) {
	if s == nil || files == nil || publisher == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradeOrganizationIngestionRepository{store: s, files: files, publisher: publisher}, nil
}

type upgradeIngestionRow struct {
	Operation uuid.UUID       `json:"operation_id"`
	Job       uuid.UUID       `json:"job_id"`
	File      uuid.UUID       `json:"file_id"`
	Fact      uuid.UUID       `json:"fact_id"`
	At        time.Time       `json:"ingested_at"`
	FileRow   json.RawMessage `json:"file_snapshot"`
	Mapping   json.RawMessage `json:"mapping_snapshot"`
	FactRow   json.RawMessage `json:"fact_snapshot"`
}

func ingestionReceipt(raw json.RawMessage) (*ports.MovieUpgradeOrganizationIngestionResult, error) {
	var i upgradeIngestionRow
	if json.Unmarshal(raw, &i) != nil || i.Operation == uuid.Nil || i.Job == uuid.Nil || i.File == uuid.Nil || i.Fact == uuid.Nil || i.At.IsZero() {
		return nil, preparationConflict()
	}
	return &ports.MovieUpgradeOrganizationIngestionResult{OperationID: i.Operation, JobID: i.Job, FileID: i.File, FactID: i.Fact, IngestedAt: i.At, Status: "imported_pending_retirement", ExecutionAllowed: false}, nil
}
func (r *movieUpgradeOrganizationIngestionRepository) ledger(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub uuid.UUID, raw json.RawMessage, existing bool) (ownedUpgradeRows, error) {
	out, e := ingestionReceipt(raw)
	if e != nil {
		return nil, e
	}
	return readUpgradeObservationLedger(ctx, tx, out.OperationID, a, sub, out.JobID.String(), coverageDigest([]any{"upgrade.organization-ingest.v1", raw}), upgradeIngestionAction, out, existing)
}
func (r *movieUpgradeOrganizationIngestionRepository) recover(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub, job uuid.UUID) (*ports.MovieUpgradeOrganizationIngestionResult, error) {
	var raw json.RawMessage
	e := tx.QueryRow(ctx, `SELECT to_jsonb(i) FROM movie_upgrade_organization_ingestion i WHERE job_id=$1 AND actor_id=$2::uuid FOR SHARE`, job, a.ID).Scan(&raw)
	if e == pgx.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	own, e := r.ledger(ctx, tx, a, sub, raw, true)
	if e != nil {
		return nil, e
	}
	if e = own.verify(ctx, tx); e != nil {
		return nil, e
	}
	// Recovery validates immutable original rows, never current files or old leases.
	var valid bool
	e = tx.QueryRow(ctx, `SELECT i.baseline->'command'=to_jsonb(b) AND i.baseline->'claim'=to_jsonb(c) AND i.baseline->'permit'=to_jsonb(p) AND i.baseline->'result'=to_jsonb(r) AND i.baseline->'preparation'=to_jsonb(prepared) AND i.baseline->'job'=to_jsonb(j) AND pulse_upgrade_organization_execution_finished(i.job_id,to_jsonb(j)) FROM movie_upgrade_organization_ingestion i JOIN movie_upgrade_organization_job b USING(job_id) JOIN movie_upgrade_organization_job_claim c USING(job_id) JOIN movie_upgrade_organization_execution_permit p USING(job_id) JOIN movie_upgrade_organization_execution_result r ON r.permit_id=p.permit_id JOIN movie_upgrade_organization_preparation prepared ON prepared.id=b.preparation_id JOIN job j ON j.id=i.job_id WHERE i.job_id=$1`, job).Scan(&valid)
	if e != nil {
		return nil, e
	}
	if !valid {
		return nil, preparationConflict()
	}
	return ingestionReceipt(raw)
}
func (r *movieUpgradeOrganizationIngestionRepository) authorize(ctx context.Context, tx pgx.Tx, a *ports.Actor, job uuid.UUID) (*ports.Actor, uuid.UUID, uuid.UUID, error) {
	var op uuid.UUID
	if e := tx.QueryRow(ctx, `SELECT operation_id FROM movie_upgrade_organization_job WHERE job_id=$1`, job).Scan(&op); e != nil {
		return nil, uuid.Nil, uuid.Nil, e
	}
	a, sub, e := r.store.authorizePreparation(ctx, tx, a, op)
	return a, sub, op, e
}
func (r *movieUpgradeOrganizationIngestionRepository) ReadMovieUpgradeOrganizationIngestionReceipt(ctx context.Context, a *ports.Actor, job uuid.UUID) (out *ports.MovieUpgradeOrganizationIngestionResult, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if job == uuid.Nil {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := r.store.pool.Begin(work)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	a, sub, _, e := r.authorize(work, tx, a, job)
	if e != nil {
		return nil, e
	}
	out, e = r.recover(work, tx, a, sub, job)
	if e != nil {
		return nil, e
	}
	if _, _, _, e = r.authorize(work, tx, a, job); e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return out, nil
}

func (r *movieUpgradeOrganizationIngestionRepository) inputs(ctx context.Context, tx pgx.Tx, a *ports.Actor, job uuid.UUID) (upgradeOrganizationPhysical, string, error) {
	a, _, op, e := r.authorize(ctx, tx, a, job)
	if e != nil {
		return upgradeOrganizationPhysical{}, "", e
	}
	var prep, obs uuid.UUID
	var raw json.RawMessage
	var marker string
	e = tx.QueryRow(ctx, `SELECT b.preparation_id,a.observation_id,p.snapshot->'physical',root.marker_filename FROM movie_upgrade_organization_job b JOIN movie_upgrade_organization_preparation p ON p.id=b.preparation_id JOIN movie_upgrade_organization_authorization a ON a.id=p.authorization_id JOIN library_root root ON root.id=a.root_id WHERE b.job_id=$1 FOR SHARE OF b,p,a`, job).Scan(&prep, &obs, &raw, &marker)
	if e != nil {
		return upgradeOrganizationPhysical{}, "", e
	}
	logical := movieUpgradeOrganizationJobRepository{store: r.store}
	if _, _, e = logical.basis(ctx, tx, a, op, prep); e != nil {
		return upgradeOrganizationPhysical{}, "", e
	}
	if _, e = r.store.organizationBasis(ctx, tx, a, op, obs); e != nil {
		return upgradeOrganizationPhysical{}, "", e
	}
	var physical upgradeOrganizationPhysical
	if json.Unmarshal(raw, &physical) != nil {
		return physical, "", preparationConflict()
	}
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	return physical, marker, nil
}

func (r *movieUpgradeOrganizationIngestionRepository) IngestMovieUpgradeOrganization(ctx context.Context, a *ports.Actor, job uuid.UUID) (out *ports.MovieUpgradeOrganizationIngestionResult, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if job == uuid.Nil {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	if out, err = r.ReadMovieUpgradeOrganizationIngestionReceipt(ctx, a, job); err != nil || out != nil {
		return out, err
	}
	execution := movieUpgradeOrganizationExecutionRepository{store: r.store, files: r.files}
	returned, e := execution.ReadMovieUpgradeOrganizationExecutionReceipt(ctx, a, job)
	if e != nil {
		return nil, e
	}
	if returned == nil || returned.Status != "linked_pending_ingestion" {
		return nil, preparationConflict()
	}
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	tx, e := r.store.pool.Begin(work)
	if e != nil {
		cancel()
		return nil, e
	}
	a, sub, _, e := r.authorize(work, tx, a, job)
	if e != nil {
		upgradeRollback(work, tx)
		cancel()
		return nil, e
	}
	if out, e = r.recover(work, tx, a, sub, job); e != nil {
		upgradeRollback(work, tx)
		cancel()
		return nil, e
	} else if out != nil {
		e = tx.Commit(work)
		cancel()
		return out, e
	}
	physical, marker, e := r.inputs(work, tx, a, job)
	if e == nil {
		e = tx.Commit(work)
	} else {
		upgradeRollback(work, tx)
	}
	cancel()
	if e != nil {
		return nil, e
	}
	roots, release, e := r.files.LockRoots(ctx, physical.RootPath)
	if e != nil {
		return nil, e
	}
	defer release()
	root := roots[physical.RootPath]
	if root == nil {
		return nil, ports.ErrFileIdentity
	}
	var observed time.Time
	if e = r.store.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&observed); e != nil {
		return nil, e
	}
	id, e := root.Identity(ctx)
	if e != nil || id.Device != physical.Root.Device || id.Inode != physical.Root.Inode {
		return nil, ports.ErrFileIdentity
	}
	if _, e = root.Inspect(ctx, marker); e != nil {
		return nil, e
	}
	old, e := root.Inspect(ctx, physical.OldRel)
	if e != nil || old != physical.Old {
		return nil, ports.ErrFileIdentity
	}
	target, e := root.Inspect(ctx, physical.TargetRel)
	if e != nil || target != physical.Source {
		return nil, ports.ErrFileIdentity
	}
	work, cancel = context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e = r.store.pool.Begin(work)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	a, sub, op, e := r.authorize(work, tx, a, job)
	if e != nil {
		return nil, e
	}
	if out, e = r.recover(work, tx, a, sub, job); e != nil {
		return nil, e
	} else if out != nil {
		if e = tx.Commit(work); e != nil {
			return nil, e
		}
		return out, nil
	}
	current, currentMarker, e := r.inputs(work, tx, a, job)
	if e != nil {
		return nil, e
	}
	if current != physical || currentMarker != marker {
		return nil, preparationConflict()
	}
	var baseline json.RawMessage
	fileID, factID := r.store.ids.NewUUID(), r.store.ids.NewUUID()
	if e = tx.QueryRow(work, `SELECT pulse_upgrade_ingestion_state($1,$2,$3)`, op, fileID, factID).Scan(&baseline); e != nil {
		return nil, e
	}
	var expected json.RawMessage
	fingerprint := fmt.Sprintf("stat.v1:%d:%d:%d", target.Inode, target.Size, target.Mtime)
	e = tx.QueryRow(work, `WITH t AS(SELECT clock_timestamp() n),a AS(SELECT * FROM movie_upgrade_authority WHERE operation_id=$1),p AS(SELECT * FROM movie_upgrade_organization_preparation WHERE operation_id=$1) SELECT jsonb_build_object('operation_id',$1::uuid,'job_id',$2::uuid,'actor_id',$3::uuid,'file_id',$4::uuid,'fact_id',$5::uuid,'baseline',$6::jsonb,'observed_at',$7::timestamptz,'ingested_at',n,'status','imported_pending_retirement','execution_allowed',false,'file_snapshot',to_jsonb(jsonb_populate_record(NULL::library_file,jsonb_build_object('id',$4::uuid,'root_id',a.root_id,'rel_path',$8::text,'size_bytes',$9::bigint,'mtime_ns',$10::bigint,'file_fingerprint',$11::text,'last_mutation_seq',($6::jsonb->'root'->>'mutation_seq')::bigint+1,'mapping_revision',2,'created_at',n))),'mapping_snapshot',to_jsonb(jsonb_populate_record(NULL::library_file_unit,jsonb_build_object('file_id',$4::uuid,'unit_id',a.unit_id,'created_at',n))),'fact_snapshot',to_jsonb(jsonb_populate_record(NULL::unit_fact,jsonb_build_object('id',$5::uuid,'unit_id',a.unit_id,'root_id',a.root_id,'stage','imported','source_file_id',$4::uuid,'quality_profile',p.step_snapshot->'quality_profile','created_at',n)))) FROM t,a,p`, op, job, a.ID, fileID, factID, baseline, observed, physical.TargetRel, target.Size, target.Mtime, fingerprint).Scan(&expected)
	if e != nil {
		return nil, e
	}
	var actual json.RawMessage
	if e = tx.QueryRow(work, `INSERT INTO movie_upgrade_organization_ingestion SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_organization_ingestion,$1::jsonb) RETURNING to_jsonb(movie_upgrade_organization_ingestion)`, expected).Scan(&actual); e != nil {
		return nil, e
	}
	if !preparationSameJSON(actual, expected) {
		return nil, preparationConflict()
	}
	var row upgradeIngestionRow
	if json.Unmarshal(expected, &row) != nil {
		return nil, preparationConflict()
	}
	var initialFile json.RawMessage
	if e = tx.QueryRow(work, `SELECT jsonb_set($1::jsonb,'{mapping_revision}','1')`, row.FileRow).Scan(&initialFile); e != nil {
		return nil, e
	}
	for _, effect := range []struct {
		table string
		raw   json.RawMessage
	}{{"library_file", initialFile}, {"library_file_unit", row.Mapping}, {"unit_fact", row.FactRow}} {
		if e = tx.QueryRow(work, "INSERT INTO "+effect.table+" SELECT * FROM jsonb_populate_record(NULL::"+effect.table+",$1::jsonb) RETURNING to_jsonb("+effect.table+")", effect.raw).Scan(&actual); e != nil {
			return nil, e
		}
		if !preparationSameJSON(actual, effect.raw) {
			return nil, preparationConflict()
		}
	}
	if _, e = tx.Exec(work, `UPDATE library_root SET mutation_seq=mutation_seq+1,updated_at=$2 WHERE id=(SELECT root_id FROM movie_upgrade_authority WHERE operation_id=$1)`, op, row.At); e != nil {
		return nil, e
	}
	var imported struct {
		UnitID uuid.UUID `json:"unit_id"`
	}
	if json.Unmarshal(row.FactRow, &imported) != nil || imported.UnitID == uuid.Nil {
		return nil, preparationConflict()
	}
	if _, e = r.publisher.PublishImported(work, db.New(tx), imported.UnitID.String(), map[string]any{"operation_id": op, "file_id": fileID, "unit_id": imported.UnitID}); e != nil {
		return nil, e
	}
	own, e := r.ledger(work, tx, a, sub, expected, false)
	if e != nil {
		return nil, e
	}
	out, e = ingestionReceipt(expected)
	if e != nil {
		return nil, e
	}
	check := func() error {
		if _, _, e := r.store.authorizePreparation(work, tx, a, op); e != nil {
			return e
		}
		if e := own.verify(work, tx); e != nil {
			return e
		}
		var same bool
		if e := tx.QueryRow(work, `SELECT to_jsonb(i)=$2::jsonb FROM movie_upgrade_organization_ingestion i WHERE operation_id=$1`, op, expected).Scan(&same); e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}
		_, e := tx.Exec(work, `SELECT pulse_assert_upgrade_ingestion($1)`, op)
		return e
	}
	ledger := UpgradeReservationStore{pool: r.store.pool, ids: r.store.ids}
	if e = ledger.upgradeSuccess(work, tx, a, sub, "upgrade.organization-ingest:"+job.String(), coverageDigest([]any{"upgrade.organization-ingest.v1", expected}), upgradeIngestionAction, op, out, own, check); e != nil {
		return nil, e
	}
	if _, e = r.ledger(work, tx, a, sub, expected, true); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return out, nil
}
