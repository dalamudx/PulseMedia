package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UntrackedScopeStore struct {
	pool  *pgxpool.Pool
	ids   ports.IDGen
	clock ports.Clock
}

var _ ports.UntrackedScopeRepository = (*UntrackedScopeStore)(nil)

func NewUntrackedScopeStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock) *UntrackedScopeStore {
	return &UntrackedScopeStore{pool: pool, ids: ids, clock: clock}
}

func untrackedGuard(ctx context.Context, q *db.Queries, a *ports.Actor) error {
	if err := watchGuard(ctx, q); err != nil {
		return err
	}
	return lockAcquisitionUser(ctx, q, a)
}

func (s *UntrackedScopeStore) begin(ctx context.Context, a *ports.Actor) (pgx.Tx, *db.Queries, error) {
	if s == nil || s.pool == nil || s.ids == nil || s.clock == nil {
		return nil, nil, ports.ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, nil, err
	}
	q := db.New(tx)
	if err = untrackedGuard(ctx, q, a); err != nil {
		managementRollback(ctx, tx)
		return nil, nil, err
	}
	return tx, q, nil
}

func (s *UntrackedScopeStore) ReadUntrackedScope(ctx context.Context, actor *ports.Actor, id uuid.UUID) (out *ports.UntrackedScope, err error) {
	defer func() { err = untrackedError(ctx, err) }()
	a, err := checkinActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	out, err = untrackedCurrent(ctx, tx, uuid.MustParse(a.ID), id, false)
	if err != nil {
		return nil, err
	}
	if err = untrackedGuard(ctx, q, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *UntrackedScopeStore) ReadUntrackedScopeReceipt(ctx context.Context, actor *ports.Actor, id uuid.UUID, key string) (out *ports.UntrackedScopeReceipt, err error) {
	defer func() { err = untrackedError(ctx, err) }()
	a, err := checkinActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil || !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	owner := uuid.MustParse(a.ID)
	current, err := untrackedCurrent(ctx, tx, owner, id, false)
	if err != nil {
		return nil, err
	}
	out, _, err = untrackedReceipt(ctx, tx, owner, id, key, current)
	if err != nil {
		return nil, err
	}
	if err = untrackedGuard(ctx, q, a); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *UntrackedScopeStore) PrepareUntrackedScope(ctx context.Context, actor *ports.Actor, id uuid.UUID, in ports.UntrackedScopeInput, key string) (out *ports.UntrackedScopePreparation, err error) {
	defer func() { err = untrackedError(ctx, err) }()
	a, err := checkinActor(actor)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, a)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	owner := uuid.MustParse(a.ID)
	current, err := untrackedCurrent(ctx, tx, owner, id, false)
	if err != nil {
		return nil, err
	}
	if !untrackedValid(in) || !doubanSyncASCII(key, 160, false) {
		return nil, ports.ErrRejected
	}
	hash := untrackedHash(in)
	receipt, oldHash, err := untrackedReceipt(ctx, tx, owner, id, key, current)
	if err == nil {
		if hash != oldHash {
			return nil, ports.ErrEndpointVersionConflict
		}
		if err = untrackedGuard(ctx, q, a); err != nil {
			return nil, err
		}
		return &ports.UntrackedScopePreparation{Receipt: receipt}, tx.Commit(ctx)
	}
	if !errors.Is(err, ports.ErrNotFound) {
		return nil, err
	}
	if (current == nil && in.ExpectedVersion != 0) || (current != nil && current.Version != in.ExpectedVersion) {
		return nil, ports.ErrEndpointVersionConflict
	}
	retained := untrackedRetain(in, current)
	var material ports.UntrackedScopeMaterial
	if retained {
		material = untrackedOldMaterial(*current)
	} else {
		material, err = untrackedMaterial(ctx, q, owner, id, in)
		if err != nil {
			return nil, err
		}
	}
	// No rows are written and no filesystem capability is opened during Prepare.
	if err = untrackedLockSelection(ctx, tx, material); err != nil {
		return nil, err
	}
	if !retained {
		again, e := untrackedMaterial(ctx, q, owner, id, in)
		if e != nil {
			return nil, e
		}
		if !reflect.DeepEqual(material, again) {
			return nil, ports.ErrEndpointVersionConflict
		}
	}
	again, err := untrackedCurrent(ctx, tx, owner, id, false)
	if err != nil {
		return nil, err
	}
	if (current == nil) != (again == nil) || (current != nil && !untrackedSame(*current, *again)) {
		return nil, ports.ErrEndpointVersionConflict
	}
	var issued time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&issued); err != nil {
		return nil, err
	}
	if err = untrackedGuard(ctx, q, a); err != nil {
		return nil, err
	}
	c := &untrackedScopeCapture{store: s, actor: *a, input: in, key: key, hash: hash, material: material, previous: current, retained: retained, deadline: issued.Add(untrackedScopeTTL), issuedLocal: s.clock.Now()}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ports.UntrackedScopePreparation{Capture: c}, nil
}

func (s *UntrackedScopeStore) validateCapture(ctx context.Context, tx pgx.Tx, q *db.Queries, c *untrackedScopeCapture) error {
	if err := untrackedGuard(ctx, q, &c.actor); err != nil {
		return err
	}
	if c.hash != untrackedHash(c.input) {
		return ports.ErrEndpointVersionConflict
	}
	if !c.retained {
		material, err := untrackedMaterial(ctx, q, c.material.OwnerID, c.material.ScopeID, c.input)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(material, c.material) {
			return ports.ErrEndpointVersionConflict
		}
	} else if !untrackedRetain(c.input, c.previous) {
		return ports.ErrEndpointVersionConflict
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if !now.Before(c.deadline) || s.clock.Since(c.issuedLocal) >= untrackedScopeTTL {
		return ports.ErrEndpointVersionConflict
	}
	return nil
}

func (s *UntrackedScopeStore) CommitUntrackedScope(ctx context.Context, capture ports.UntrackedScopeCapture, evidence ports.UntrackedScopeEvidence) (out *ports.UntrackedScopeReceipt, err error) {
	defer func() { err = untrackedError(ctx, err) }()
	c, ok := capture.(*untrackedScopeCapture)
	if !ok || c == nil || c.store != s || s == nil {
		return nil, ports.ErrRejected
	}
	tx, q, err := s.begin(ctx, &c.actor)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	if err = untrackedLockSelection(ctx, tx, c.material); err != nil {
		return nil, err
	}
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "untracked_directory_scope", AggregateID: c.material.ScopeID.String()}); err != nil {
		return nil, err
	}
	current, err := untrackedCurrent(ctx, tx, c.material.OwnerID, c.material.ScopeID, true)
	if err != nil {
		return nil, err
	}
	receipt, hash, err := untrackedReceipt(ctx, tx, c.material.OwnerID, c.material.ScopeID, c.key, current)
	if err == nil {
		if hash != c.hash {
			return nil, ports.ErrEndpointVersionConflict
		}
		if err = untrackedGuard(ctx, q, &c.actor); err != nil {
			return nil, err
		}
		return receipt, tx.Commit(ctx)
	}
	if !errors.Is(err, ports.ErrNotFound) {
		return nil, err
	}
	if (current == nil) != (c.previous == nil) || (current != nil && !untrackedSame(*current, *c.previous)) {
		return nil, ports.ErrEndpointVersionConflict
	}
	if err = s.validateCapture(ctx, tx, q, c); err != nil {
		return nil, err
	}
	m := c.material
	if c.input.Enabled && (evidence.SourceIdentity.Device < 0 || evidence.SourceIdentity.Inode <= 0 || untrackedIdentity(evidence.TargetIdentity) != m.TargetIdentity) {
		return nil, ports.ErrRejected
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	saved := ports.UntrackedScope{ID: m.ScopeID, OwnerID: m.OwnerID, Enabled: c.input.Enabled, Version: c.input.ExpectedVersion + 1, ClientID: m.ClientID, ClientVersion: m.ClientVersion, ClientStamp: m.ClientStamp, RemotePrefix: m.RemotePrefix, LocalRoot: m.LocalRoot, TargetRootID: m.TargetRoot.ID, TargetRootPath: m.TargetRoot.Path, TargetRootGeneration: m.TargetRoot.Generation, TargetMediaType: m.TargetRoot.MediaType, TargetMarker: m.TargetRoot.MarkerFilename, TargetBaselineScanID: m.TargetBaselineScanID, TargetIdentity: m.TargetIdentity, CreatedAt: now, UpdatedAt: now}
	if saved.Enabled {
		saved.SourceIdentity = untrackedIdentity(evidence.SourceIdentity)
	}
	if current != nil {
		saved.CreatedAt = current.CreatedAt
		if c.retained {
			saved.SourceIdentity = current.SourceIdentity
		}
	}
	if !untrackedScopeValid(saved) {
		return nil, ports.ErrRejected
	}
	raw, _ := json.Marshal(saved)
	input, _ := json.Marshal(c.input)
	if current == nil {
		err = q.InsertUntrackedScope(ctx, db.InsertUntrackedScopeParams{ID: saved.ID, OwnerID: saved.OwnerID, Enabled: saved.Enabled, Version: saved.Version, ClientID: saved.ClientID, TargetRootID: saved.TargetRootID, LocalRoot: saved.LocalRoot, Snapshot: raw, CreatedAt: pgtype.Timestamptz{Time: saved.CreatedAt, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: saved.UpdatedAt, Valid: true}})
	} else {
		n, e := q.UpdateUntrackedScope(ctx, db.UpdateUntrackedScopeParams{ID: saved.ID, OwnerID: saved.OwnerID, Enabled: saved.Enabled, ClientID: saved.ClientID, TargetRootID: saved.TargetRootID, LocalRoot: saved.LocalRoot, Snapshot: raw, UpdatedAt: pgtype.Timestamptz{Time: saved.UpdatedAt, Valid: true}, ExpectedVersion: c.input.ExpectedVersion})
		err = e
		if err == nil && n != 1 {
			err = ports.ErrEndpointVersionConflict
		}
	}
	if err != nil {
		return nil, err
	}
	if err = q.InsertUntrackedScopeReceipt(ctx, db.InsertUntrackedScopeReceiptParams{ScopeID: saved.ID, OwnerID: saved.OwnerID, CommandKey: c.key, Version: saved.Version, Enabled: saved.Enabled, LocalRoot: saved.LocalRoot, RequestHash: c.hash, Input: input, Snapshot: raw}); err != nil {
		return nil, err
	}
	auditID, eventID := s.ids.NewUUID(), s.ids.NewUUID()
	audit, err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: auditID, ActorType: string(c.actor.Type), ActorID: c.actor.ID, Action: "untracked.scope.saved", TargetType: "untracked_directory_scope", TargetID: saved.ID.String(), Payload: raw})
	if err != nil {
		return nil, err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "untracked_directory_scope", AggregateID: saved.ID.String()})
	if err != nil {
		return nil, err
	}
	event, err := q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: eventID, AggregateType: "untracked_directory_scope", AggregateID: saved.ID.String(), EventType: "untracked.scope.saved", Seq: int64(seq), Payload: raw})
	if err != nil {
		return nil, err
	}
	var auditScope, eventScope ports.UntrackedScope
	if audit.ID != auditID || audit.ActorType != string(c.actor.Type) || audit.ActorID != c.actor.ID || audit.Action != "untracked.scope.saved" || audit.TargetType != "untracked_directory_scope" || audit.TargetID != saved.ID.String() || !audit.CreatedAt.Valid || checkinDecode(audit.Payload, &auditScope) != nil || !untrackedSame(auditScope, saved) || event.ID != eventID || event.AggregateType != "untracked_directory_scope" || event.AggregateID != saved.ID.String() || event.EventType != "untracked.scope.saved" || event.Seq != int64(seq) || !event.CreatedAt.Valid || checkinDecode(event.Payload, &eventScope) != nil || !untrackedSame(eventScope, saved) {
		return nil, ports.ErrEndpointVersionConflict
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return nil, err
	}
	if err = s.validateCapture(ctx, tx, q, c); err != nil {
		return nil, err
	}
	final, err := untrackedCurrent(ctx, tx, saved.OwnerID, saved.ID, true)
	if err != nil {
		return nil, err
	}
	if final == nil || !untrackedSame(saved, *final) {
		return nil, ports.ErrEndpointVersionConflict
	}
	original, originalHash, err := untrackedReceipt(ctx, tx, saved.OwnerID, saved.ID, c.key, final)
	if err != nil {
		return nil, err
	}
	if originalHash != c.hash || !untrackedSame(saved, original.Scope) {
		return nil, ports.ErrEndpointVersionConflict
	}
	// Capture full rows after INSERT and compare again after all deferred effects.
	var auditSame, eventSame bool
	if err = tx.QueryRow(ctx, `SELECT id=$1 AND actor_type=$2 AND actor_id=$3 AND action=$4 AND target_type=$5 AND target_id=$6 AND payload=$7::jsonb AND created_at=$8 FROM audit_log WHERE id=$1`, audit.ID, audit.ActorType, audit.ActorID, audit.Action, audit.TargetType, audit.TargetID, audit.Payload, audit.CreatedAt).Scan(&auditSame); err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, `SELECT id=$1 AND aggregate_type=$2 AND aggregate_id=$3 AND event_type=$4 AND seq=$5 AND payload=$6::jsonb AND created_at=$7 FROM outbox_event WHERE id=$1`, event.ID, event.AggregateType, event.AggregateID, event.EventType, event.Seq, event.Payload, event.CreatedAt).Scan(&eventSame); err != nil {
		return nil, err
	}
	if !auditSame || !eventSame {
		return nil, ports.ErrEndpointVersionConflict
	}
	if err = s.validateCapture(ctx, tx, q, c); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return original, nil
}
