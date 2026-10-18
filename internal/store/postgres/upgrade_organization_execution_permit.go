package postgres

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

type organizationPhysicalPermit struct {
	ID        uuid.UUID `json:"permit_id"`
	Job       uuid.UUID `json:"job_id"`
	Operation uuid.UUID `json:"operation_id"`
	Until     time.Time `json:"lease_until"`
	At        time.Time `json:"permitted_at"`
}

func (s *organizationExecution) permitLedger(ctx context.Context, tx pgx.Tx, b *organizationExecutionBasis, raw json.RawMessage, existing bool) (ownedUpgradeRows, error) {
	var out organizationPhysicalPermit
	if json.Unmarshal(raw, &out) != nil || out.ID == uuid.Nil {
		return nil, preparationConflict()
	}
	return readUpgradeObservationLedger(ctx, tx, s.base.operation, b.actor, b.sub, s.lease.ID.String(), coverageDigest([]any{"upgrade.organization-physical-permit.v1", raw}), "upgrade.organization-physical-permit", &out, existing)
}
func (s *organizationExecution) admit(ctx context.Context, observedBasis *organizationExecutionBasis, observed time.Time) error {
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := s.base.conn.Begin(work)
	if e != nil {
		return e
	}
	defer upgradeRollback(work, tx)
	b, e := s.load(work, tx)
	if e != nil {
		return e
	}
	if !preparationSameJSON(b.command, observedBasis.command) || !preparationSameJSON(b.claim, observedBasis.claim) || !preparationSameJSON(b.prepared, observedBasis.prepared) {
		return preparationConflict()
	}
	var claim struct {
		ID uuid.UUID `json:"claim_id"`
	}
	if json.Unmarshal(b.claim, &claim) != nil {
		return preparationConflict()
	}
	fields, e := json.Marshal(map[string]any{"operation_id": s.base.operation, "permit_id": s.repo.store.ids.NewUUID(), "job_id": s.lease.ID, "claim_id": claim.ID, "actor_id": b.actor.ID, "session_nonce": s.base.nonce, "advisory_key": s.base.key, "observed_at": observed, "snapshot": map[string]any{"command": b.command, "claim": b.claim, "preparation": b.prepared}})
	if e != nil {
		return e
	}
	var expected json.RawMessage
	e = tx.QueryRow(work, `SELECT to_jsonb(jsonb_populate_record(NULL::movie_upgrade_organization_execution_permit,$1::jsonb||jsonb_build_object('backend_pid',pg_backend_pid(),'backend_start',backend_start,'permitted_at',n,'lease_until',LEAST(n+interval '60 seconds',($2::jsonb->>'lease_until')::timestamptz)))) FROM pg_stat_activity,(SELECT clock_timestamp() n)t WHERE pid=pg_backend_pid()`, fields, b.job).Scan(&expected)
	if e != nil {
		return e
	}
	var actual json.RawMessage
	e = tx.QueryRow(work, `INSERT INTO movie_upgrade_organization_execution_permit SELECT * FROM jsonb_populate_record(NULL::movie_upgrade_organization_execution_permit,$1::jsonb) RETURNING to_jsonb(movie_upgrade_organization_execution_permit)`, expected).Scan(&actual)
	if e != nil {
		return e
	}
	if !preparationSameJSON(actual, expected) {
		return preparationConflict()
	}
	own, e := s.permitLedger(work, tx, b, expected, false)
	if e != nil {
		return e
	}
	var admitted organizationPhysicalPermit
	if json.Unmarshal(expected, &admitted) != nil {
		return preparationConflict()
	}
	ledger := &UpgradeReservationStore{pool: s.repo.store.pool, ids: s.repo.store.ids}
	check := func() error {
		if e := s.check(work, tx, b, b.job); e != nil {
			return e
		}
		if e := own.verify(work, tx); e != nil {
			return e
		}
		return s.checkPermit(work, tx, expected)
	}
	if e = ledger.upgradeSuccess(work, tx, b.actor, b.sub, "upgrade.organization-physical-permit:"+s.lease.ID.String(), coverageDigest([]any{"upgrade.organization-physical-permit.v1", expected}), "upgrade.organization-physical-permit", s.base.operation, &admitted, own, check); e != nil {
		return e
	}
	if _, e = s.permitLedger(work, tx, b, expected, true); e != nil {
		return e
	}
	if e = s.check(work, tx, b, b.job); e != nil {
		return e
	}
	if e = own.verify(work, tx); e != nil {
		return e
	}
	if e = s.checkPermit(work, tx, expected); e != nil {
		return e
	}
	if e = s.checkFresh(work, tx); e != nil {
		return e
	}
	if e = tx.Commit(work); e != nil {
		return e
	}
	s.permit = expected
	return nil
}
func (s *organizationExecution) checkPermit(ctx context.Context, tx pgx.Tx, expected json.RawMessage) error {
	var same bool
	e := tx.QueryRow(ctx, `SELECT to_jsonb(p)=$2::jsonb AND p.lease_until>clock_timestamp() FROM movie_upgrade_organization_execution_permit p WHERE operation_id=$1 FOR SHARE`, s.base.operation, expected).Scan(&same)
	if e != nil {
		return e
	}
	if !same {
		return preparationConflict()
	}
	_, e = tx.Exec(ctx, `SELECT pulse_upgrade_organization_execution_session(p) FROM movie_upgrade_organization_execution_permit p WHERE operation_id=$1`, s.base.operation)
	return e
}
func (s *organizationExecution) ready(ctx context.Context) error {
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := s.base.conn.Begin(work)
	if e != nil {
		return e
	}
	defer upgradeRollback(work, tx)
	b, e := s.load(work, tx)
	if e != nil {
		return e
	}
	own, e := s.permitLedger(work, tx, b, s.permit, true)
	if e != nil {
		return e
	}
	if e = own.verify(work, tx); e != nil {
		return e
	}
	if e = s.checkPermit(work, tx, s.permit); e != nil {
		return e
	}
	if e = s.checkFresh(work, tx); e != nil {
		return e
	}
	return tx.Commit(work)
}

func (s *organizationExecution) checkFresh(ctx context.Context, tx pgx.Tx) error {
	var fresh bool
	e := tx.QueryRow(ctx, `SELECT observed_at<=clock_timestamp() AND observed_at+interval '30 seconds'>clock_timestamp() FROM movie_upgrade_organization_execution_permit WHERE operation_id=$1`, s.base.operation).Scan(&fresh)
	if e != nil {
		return e
	}
	if !fresh {
		return preparationConflict()
	}
	return nil
}
