package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PolicyStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

var _ ports.PolicyRepository = (*PolicyStore)(nil)

func NewPolicyStore(pool *pgxpool.Pool, ids ports.IDGen) *PolicyStore { return &PolicyStore{pool, ids} }
func policyError(ctx context.Context, e error) error {
	if e == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(e, ports.ErrPolicyConflict) || errors.Is(e, ports.ErrInvalidPolicy) || errors.Is(e, ports.ErrForbidden) || errors.Is(e, ports.ErrUnauthorized) {
		return e
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	return ports.ErrUnavailable
}
func (s *PolicyStore) ListPolicies(ctx context.Context) (documents []ports.PolicyDocument, err error) {
	defer func() { err = policyError(ctx, err) }()
	rows, e := db.New(s.pool).ListPolicyDocuments(ctx)
	if e != nil {
		return nil, e
	}
	documents = []ports.PolicyDocument{}
	for _, r := range rows {
		var rules quality.PolicyRules
		if e = json.Unmarshal(r.Rules, &rules); e != nil {
			return nil, e
		}
		documents = append(documents, ports.PolicyDocument{ID: r.ID, Name: r.Name, Revision: r.CurrentRevision, Rules: rules})
	}
	return documents, nil
}
func (s *PolicyStore) ReadPolicy(ctx context.Context, id string, revision int64) (document *ports.PolicyDocument, err error) {
	defer func() { err = policyError(ctx, err) }()
	q := db.New(s.pool)
	p, e := q.GetPolicyByID(ctx, id)
	if e != nil {
		return nil, e
	}
	if revision == 0 {
		revision = p.CurrentRevision
	}
	r, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: id, Revision: revision})
	if e != nil {
		return nil, e
	}
	var rules quality.PolicyRules
	if e = json.Unmarshal(r.Rules, &rules); e != nil {
		return nil, e
	}
	return &ports.PolicyDocument{ID: id, Name: p.Name, Revision: revision, Rules: rules}, nil
}
func (s *PolicyStore) WritePolicy(ctx context.Context, a *ports.Actor, c ports.PolicyCommand, key, hash string) (result *ports.PolicyReceipt, err error) {
	defer func() { err = policyError(ctx, err) }()
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if acquisitionCommandActor(a) != nil || !a.IsAdmin() {
		return nil, ports.ErrForbidden
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	if e = lockAcquisitionUser(ctx, q, a); e != nil {
		return nil, e
	}
	// Serialize create vs publish/restore on the same ID, then the policy row.
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "policy.command.id", AggregateID: c.ID}); e != nil {
		return nil, e
	}
	if key != "" {
		lock, _ := json.Marshal([]string{string(a.Type), a.ID, key})
		if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "policy.command.key", AggregateID: string(lock)}); e != nil {
			return nil, e
		}
		old, e := q.GetIdempotencyRecord(ctx, db.GetIdempotencyRecordParams{ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key})
		if e == nil {
			var saved ports.PolicyReceipt
			if old.RequestHash != hash || json.Unmarshal(old.ResponseBody, &saved) != nil || saved.ID != c.ID {
				return nil, ports.ErrPolicyConflict
			}
			return &saved, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
	}
	var revision int64
	var rules quality.PolicyRules
	if c.Kind == "create" {
		if _, e = q.GetPolicyByID(ctx, c.ID); e == nil {
			return nil, ports.ErrPolicyConflict
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		if _, e = q.CreatePolicy(ctx, db.CreatePolicyParams{ID: c.ID, Name: c.Name}); e != nil {
			return nil, e
		}
		revision = 1
		if c.Rules == nil {
			return nil, ports.ErrInvalidPolicy
		}
		rules = *c.Rules
	} else {
		p, e := q.LockSearchPolicy(ctx, c.ID)
		if e != nil {
			return nil, e
		}
		if c.ExpectedRevision != -1 && p.CurrentRevision != c.ExpectedRevision {
			return nil, ports.ErrPolicyConflict
		}
		revision = p.CurrentRevision + 1
		if c.Kind == "restore" {
			if c.RestoreRevision == nil {
				return nil, ports.ErrInvalidPolicy
			}
			old, e := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: c.ID, Revision: *c.RestoreRevision})
			if e != nil {
				return nil, e
			}
			if e = json.Unmarshal(old.Rules, &rules); e != nil {
				return nil, e
			}
		} else if c.Kind == "publish" && c.Rules != nil {
			rules = *c.Rules
		} else {
			return nil, ports.ErrInvalidPolicy
		}
	}
	if quality.ValidateRules(rules) != nil {
		return nil, ports.ErrInvalidPolicy
	}
	raw, _ := json.Marshal(rules)
	if _, e = q.InsertPolicyRevision(ctx, db.InsertPolicyRevisionParams{ID: s.ids.NewUUID(), PolicyID: c.ID, Revision: revision, Rules: raw}); e != nil {
		return nil, e
	}
	if c.Kind != "create" {
		if _, e = q.BumpPolicyCurrentRevision(ctx, c.ID); e != nil {
			return nil, e
		}
		if e = q.UpdateSubscriptionsPolicyRevision(ctx, db.UpdateSubscriptionsPolicyRevisionParams{QualityPolicyID: c.ID, QualityPolicyRevision: revision}); e != nil {
			return nil, e
		}
		if e = q.InvalidateWantedItemsForPolicy(ctx, c.ID); e != nil {
			return nil, e
		}
	}
	result = &ports.PolicyReceipt{ID: c.ID, Revision: revision}
	safe, _ := json.Marshal(result)
	if _, e = q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: "policy." + c.Kind, TargetType: "policy", TargetID: c.ID, Payload: safe}); e != nil {
		return nil, e
	}
	if e = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "policy", AggregateID: c.ID}); e != nil {
		return nil, e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "policy", AggregateID: c.ID})
	if e != nil {
		return nil, e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "policy", AggregateID: c.ID, EventType: "policy.updated", Seq: int64(seq), Payload: safe}); e != nil {
		return nil, e
	}
	if key != "" {
		if _, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, IdempotencyKey: key, RequestHash: hash, ResponseCode: 201, ResponseBody: safe, ResourceID: pgtype.Text{String: c.ID, Valid: true}}); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return result, nil
}
