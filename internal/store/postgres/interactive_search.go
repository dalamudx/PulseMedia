package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InteractiveSearchStore owns durable observations; physical endpoint permits belong to the executor.
type InteractiveSearchStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewInteractiveSearchStore(pool *pgxpool.Pool, ids ports.IDGen) *InteractiveSearchStore {
	return &InteractiveSearchStore{pool: pool, ids: ids}
}

var _ ports.InteractiveSearchRepository = (*InteractiveSearchStore)(nil)

type interactiveHeader struct {
	ID             uuid.UUID      `json:"id"`
	ActorID        uuid.UUID      `json:"actor_id"`
	ActorRole      string         `json:"actor_role"`
	Query          string         `json:"query"`
	MediaID        *uuid.UUID     `json:"media_id"`
	SubscriptionID *uuid.UUID     `json:"subscription_id"`
	RankingPolicy  quality.Policy `json:"ranking_policy"`
	SourceCount    int            `json:"source_count"`
	CreatedAt      time.Time      `json:"created_at"`
	Deadline       time.Time      `json:"deadline"`
	CancelledAt    *time.Time     `json:"cancelled_at"`
	State          string         `json:"state"`
}

func (h interactiveHeader) public() *ports.InteractiveSearchHeader {
	return &ports.InteractiveSearchHeader{ID: h.ID, ActorID: h.ActorID, Query: h.Query, MediaID: h.MediaID, SubscriptionID: h.SubscriptionID, RankingPolicy: h.RankingPolicy, SourceCount: h.SourceCount, CreatedAt: h.CreatedAt.UTC(), Deadline: h.Deadline.UTC(), CancelledAt: interactiveUTC(h.CancelledAt)}
}
func interactiveUTC(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	u := v.UTC()
	return &u
}
func interactiveDecode(raw []byte, err error) (interactiveHeader, error) {
	var h interactiveHeader
	if err != nil {
		return h, err
	}
	err = json.Unmarshal(raw, &h)
	return h, err
}
func interactiveError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil || errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) || errors.Is(err, ports.ErrRejected) || errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrInteractiveSearchConflict) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	return ports.ErrUnavailable
}
func interactiveActor(ctx context.Context, tx pgx.Tx, a *ports.Actor, lock bool) error {
	if e := acquisitionCommandActor(a); e != nil {
		return e
	}
	sql := `SELECT role,is_active FROM app_user WHERE id=$1`
	if lock {
		sql += ` FOR NO KEY UPDATE`
	} else {
		sql += ` FOR SHARE`
	}
	var role string
	var active bool
	if e := tx.QueryRow(ctx, sql, a.ID).Scan(&role, &active); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return ports.ErrForbidden
		}
		return e
	}
	if !active || role != a.Role {
		return ports.ErrForbidden
	}
	return nil
}
func interactiveScope(ctx context.Context, tx pgx.Tx, a *ports.Actor, media, sub *uuid.UUID, lock bool) error {
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	if sub != nil {
		var user, mid uuid.UUID
		if e := tx.QueryRow(ctx, `SELECT user_id,media_id FROM subscription WHERE id=$1`+suffix, *sub).Scan(&user, &mid); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return ports.ErrForbidden
			}
			return e
		}
		if (a.Role != ports.RoleAdmin && user.String() != a.ID) || (media != nil && *media != mid) {
			return ports.ErrForbidden
		}
	}
	if media != nil {
		var id uuid.UUID
		if e := tx.QueryRow(ctx, `SELECT id FROM media WHERE id=$1`+suffix, *media).Scan(&id); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return ports.ErrForbidden
			}
			return e
		}
	}
	return nil
}
func interactiveOwned(ctx context.Context, tx pgx.Tx, a *ports.Actor, id uuid.UUID, lock bool) (interactiveHeader, error) {
	// Account is always the first lock, even for another user's ID.
	if e := interactiveActor(ctx, tx, a, lock); e != nil {
		return interactiveHeader{}, e
	}
	h, e := interactiveDecode(db.New(tx).InteractiveSearchReadHeader(ctx, id))
	if e != nil {
		return h, e
	}
	if h.ActorID.String() != a.ID || h.ActorRole != a.Role {
		return h, ports.ErrForbidden
	}
	if e = interactiveScope(ctx, tx, a, h.MediaID, h.SubscriptionID, true); e != nil {
		return h, e
	}
	if lock {
		return interactiveDecode(db.New(tx).InteractiveSearchLockHeader(ctx, id))
	}
	return h, nil
}
func (s *InteractiveSearchStore) begin(ctx context.Context, read bool) (pgx.Tx, error) {
	if s == nil || s.pool == nil {
		return nil, ports.ErrUnavailable
	}
	if read {
		return s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite})
	}
	return s.pool.Begin(ctx)
}

// Receipt envelopes contain the original header. Frozen policy is restored from its durable row.
func interactiveReceipt(ctx context.Context, tx pgx.Tx, a *ports.Actor, action, key, hash string, lock bool) (*ports.InteractiveSearchHeader, error) {
	var receiptID uuid.UUID
	lookup := tx.QueryRow(ctx, `SELECT h.id FROM interactive_search h JOIN idempotency_record r ON r.resource_id=h.id::text WHERE r.actor_type='user' AND r.actor_id=$1 AND r.idempotency_key=$2`, a.ID, "interactive_search."+action+":"+key).Scan(&receiptID)
	if errors.Is(lookup, pgx.ErrNoRows) {
		return nil, nil
	}
	if lookup != nil {
		return nil, lookup
	}
	if _, e := interactiveOwned(ctx, tx, a, receiptID, lock); e != nil {
		return nil, e
	}
	var raw []byte
	var oldHash string
	var code int
	var resource string
	e := tx.QueryRow(ctx, `SELECT response_body,request_hash,response_code,resource_id FROM idempotency_record WHERE actor_type='user' AND actor_id=$1 AND idempotency_key=$2`, a.ID, "interactive_search."+action+":"+key).Scan(&raw, &oldHash, &code, &resource)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var r ports.InteractiveSearchHeader
	if json.Unmarshal(raw, &r) != nil || r.ID == uuid.Nil || r.ActorID.String() != a.ID || r.ID.String() != resource || code != 202 {
		return nil, ports.ErrInteractiveSearchConflict
	}
	h, e := interactiveOwned(ctx, tx, a, r.ID, false)
	if e != nil {
		return nil, e
	}
	expected := h.public()
	expected.CancelledAt = r.CancelledAt
	expected.RankingPolicy = quality.Policy{}
	rr := r
	rr.RankingPolicy = quality.Policy{}
	// Equal compares instants, independently of JSON timestamp spelling/location.
	expected.CreatedAt = expected.CreatedAt.UTC()
	expected.Deadline = expected.Deadline.UTC()
	rr.CreatedAt = rr.CreatedAt.UTC()
	rr.Deadline = rr.Deadline.UTC()
	if !reflect.DeepEqual(*expected, rr) || (action == "create" && r.CancelledAt != nil) || (action == "cancel" && (r.CancelledAt == nil || h.CancelledAt == nil || !r.CancelledAt.Equal(*h.CancelledAt))) || (hash != "" && hash != oldHash) {
		return nil, ports.ErrInteractiveSearchConflict
	}
	r.RankingPolicy = h.RankingPolicy
	r.CreatedAt = r.CreatedAt.UTC()
	r.Deadline = r.Deadline.UTC()
	r.CancelledAt = interactiveUTC(r.CancelledAt)
	return &r, nil
}
func (s *InteractiveSearchStore) save(ctx context.Context, tx pgx.Tx, a *ports.Actor, h *ports.InteractiveSearchHeader, action, key, hash string) error {
	q := db.New(tx)
	safe, _ := json.Marshal(map[string]any{"search_id": h.ID, "state": action})
	event := "interactive_search." + action
	if _, e := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: "user", ActorID: a.ID, Action: event, TargetType: "interactive_search", TargetID: h.ID.String(), Payload: safe}); e != nil {
		return e
	}
	if e := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "interactive_search", AggregateID: h.ID.String()}); e != nil {
		return e
	}
	seq, e := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams{AggregateType: "interactive_search", AggregateID: h.ID.String()})
	if e != nil {
		return e
	}
	if _, e = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: "interactive_search", AggregateID: h.ID.String(), Seq: int64(seq), EventType: event, Payload: safe}); e != nil {
		return e
	}
	raw, _ := json.Marshal(h)
	_, e = q.CreateIdempotencyRecord(ctx, db.CreateIdempotencyRecordParams{ID: s.ids.NewUUID(), ActorType: "user", ActorID: a.ID, IdempotencyKey: event + ":" + key, RequestHash: hash, ResponseCode: 202, ResponseBody: raw, ResourceID: pgtype.Text{String: h.ID.String(), Valid: true}})
	return e
}
func (s *InteractiveSearchStore) CreateInteractiveSearch(ctx context.Context, a *ports.Actor, in ports.PreparedInteractiveSearch, key, hash string) (out *ports.InteractiveSearchHeader, err error) {
	defer func() { err = interactiveError(ctx, err) }()
	if e := acquisitionCommandActor(a); e != nil {
		return nil, e
	}
	if !managementKey(key, hash) || strings.TrimSpace(in.Query) == "" || len([]rune(in.Query)) > 512 {
		return nil, ports.ErrRejected
	}
	tx, e := s.begin(ctx, false)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	if e = interactiveActor(ctx, tx, a, true); e != nil {
		return nil, e
	}
	if e = interactiveScope(ctx, tx, a, in.MediaID, in.SubscriptionID, true); e != nil {
		return nil, e
	}
	// Serialize same actor/key before checking a receipt. Account SHARE alone permits concurrent commands.
	if e = db.New(tx).LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "interactive_search.create", AggregateID: a.ID + ":" + key}); e != nil {
		return nil, e
	}
	if out, e = interactiveReceipt(ctx, tx, a, "create", key, hash, true); e != nil {
		return nil, e
	}
	if out != nil {
		return out, tx.Commit(ctx)
	}
	// A historical original-key receipt above keeps its original strategy.
	// New observations freeze the trusted bound/default revision in this same
	// snapshot. This read takes no later policy lock after the scope locks.
	var rankingSubscription pgtype.UUID
	if in.SubscriptionID != nil {
		rankingSubscription = pgtype.UUID{Bytes: *in.SubscriptionID, Valid: true}
	}
	frozen, e := db.New(tx).GetInteractiveRankingPolicy(ctx, rankingSubscription)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	var ruleSet quality.PolicyRules
	if json.Unmarshal(frozen.Rules, &ruleSet) != nil || quality.ValidateRules(ruleSet) != nil {
		return nil, ports.ErrUnavailable
	}
	in.RankingPolicy = quality.Policy{ID: frozen.PolicyID, Revision: frozen.Revision, ResolutionOrder: ruleSet.ResolutionOrder, SourceOrder: ruleSet.SourceOrder, PreferHDR: ruleSet.PreferHDR, CustomWords: ruleSet.CustomWords}
	rows, e := tx.Query(ctx, `SELECT i.id,i.config_version FROM indexer i LEFT JOIN indexer_lifecycle l ON l.indexer_id=i.id WHERE COALESCE(l.enabled,true) AND l.deleted_at IS NULL ORDER BY i.id FOR SHARE OF i`)
	if e != nil {
		return nil, e
	}
	type source struct {
		id      uuid.UUID
		version int64
	}
	sources := []source{}
	for rows.Next() {
		var v source
		if e = rows.Scan(&v.id, &v.version); e != nil {
			rows.Close()
			return nil, e
		}
		sources = append(sources, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(sources) == 0 || len(sources) > 64 {
		return nil, ports.ErrRejected
	}
	var live int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM interactive_search WHERE actor_id=$1 AND state='running' AND cancelled_at IS NULL AND deadline>clock_timestamp()`, a.ID).Scan(&live); e != nil {
		return nil, e
	}
	if live >= 4 {
		return nil, ports.ErrRejected
	}
	if s.ids == nil {
		return nil, ports.ErrUnavailable
	}
	policy, e := json.Marshal(in.RankingPolicy)
	if e != nil {
		return nil, e
	}
	id := s.ids.NewUUID()
	var raw []byte
	e = tx.QueryRow(ctx, `INSERT INTO interactive_search(id,actor_id,actor_role,query,media_id,subscription_id,ranking_policy,source_count,created_at,deadline) VALUES($1,$2,$3,$4,$5,$6,$7,$8,statement_timestamp(),statement_timestamp()+interval '30 seconds') RETURNING to_jsonb(interactive_search)`, id, a.ID, a.Role, in.Query, in.MediaID, in.SubscriptionID, policy, len(sources)).Scan(&raw)
	// Statement time is captured after account, scope and source locks.
	if e != nil {
		return nil, e
	}
	h, e := interactiveDecode(raw, nil)
	if e != nil {
		return nil, e
	}
	for _, v := range sources {
		jobID := s.ids.NewUUID()
		payload, _ := json.Marshal(ports.InteractiveSearchSourceTask{SearchID: id, IndexerID: v.id, ConfigVersion: v.version})
		if _, e = tx.Exec(ctx, `INSERT INTO job(id,kind,dedupe_key,state,payload,max_attempts) VALUES($1,'interactive_search_source',$2,'queued',$3,1)`, jobID, id.String()+":"+v.id.String(), payload); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO interactive_search_source(search_id,indexer_id,config_version,job_id) VALUES($1,$2,$3,$4)`, id, v.id, v.version, jobID); e != nil {
			return nil, e
		}
	}
	out = h.public()
	if e = s.save(ctx, tx, a, out, "create", key, hash); e != nil {
		return nil, e
	}
	return out, tx.Commit(ctx)
}
func (s *InteractiveSearchStore) CancelInteractiveSearch(ctx context.Context, a *ports.Actor, id uuid.UUID, key, hash string) (out *ports.InteractiveSearchHeader, err error) {
	defer func() { err = interactiveError(ctx, err) }()
	if !managementKey(key, hash) {
		return nil, ports.ErrRejected
	}
	tx, e := s.begin(ctx, false)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	if _, e = interactiveOwned(ctx, tx, a, id, true); e != nil {
		return nil, e
	}
	if out, e = interactiveReceipt(ctx, tx, a, "cancel", key, hash, true); e != nil {
		return nil, e
	}
	if out != nil {
		if out.ID != id {
			return nil, ports.ErrInteractiveSearchConflict
		}
		return out, tx.Commit(ctx)
	}
	var raw []byte
	e = tx.QueryRow(ctx, `UPDATE interactive_search SET cancelled_at=COALESCE(cancelled_at,clock_timestamp()),state='cancelled' WHERE id=$1 RETURNING to_jsonb(interactive_search)`, id).Scan(&raw)
	if e != nil {
		return nil, e
	}
	h, e := interactiveDecode(raw, nil)
	if e != nil {
		return nil, e
	}
	if e = interactiveStop(ctx, tx, id, "cancelled"); e != nil {
		return nil, e
	}
	out = h.public()
	if e = s.save(ctx, tx, a, out, "cancel", key, hash); e != nil {
		return nil, e
	}
	return out, tx.Commit(ctx)
}
func (s *InteractiveSearchStore) ReadInteractiveSearchReceipt(ctx context.Context, a *ports.Actor, key, action string) (out *ports.InteractiveSearchHeader, err error) {
	defer func() { err = interactiveError(ctx, err) }()
	if (action != "create" && action != "cancel") || !managementKey(key, strings.Repeat("0", 64)) {
		return nil, ports.ErrRejected
	}
	tx, e := s.begin(ctx, true)
	if e != nil {
		return nil, e
	}
	defer managementRollback(ctx, tx)
	if e = interactiveActor(ctx, tx, a, false); e != nil {
		return nil, e
	}
	out, e = interactiveReceipt(ctx, tx, a, action, key, "", false)
	if e != nil {
		return nil, e
	}
	if out == nil {
		return nil, ports.ErrNotFound
	}
	return out, tx.Commit(ctx)
}
