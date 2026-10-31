package postgres

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TVEpisodeUpgradeStore never constructs ordinary download or movie authority.
// Every network/file invocation runs outside its narrow business transactions.
type TVEpisodeUpgradeStore struct {
	pool      *pgxpool.Pool
	ids       ports.IDGen
	decoder   ports.CredentialDecoder
	native    ports.TVEpisodeUpgradeNative
	files     ports.TransferFilesystem
	recycler  ports.RecycleFilesystem
	publisher MovieUpgradeImportedPublisher
}

func NewTVEpisodeUpgradeStore(p *pgxpool.Pool, ids ports.IDGen, decoder ports.CredentialDecoder, native ports.TVEpisodeUpgradeNative, files ports.TransferFilesystem, recycler ports.RecycleFilesystem, publisher MovieUpgradeImportedPublisher) *TVEpisodeUpgradeStore {
	return &TVEpisodeUpgradeStore{p, ids, decoder, native, files, recycler, publisher}
}

var _ ports.TVEpisodeUpgradeRepository = (*TVEpisodeUpgradeStore)(nil)

func tvUpgradeError(ctx context.Context, e error) error {
	if e == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, v := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrTVEpisodeUpgradeConflict, ports.ErrTVEpisodeUpgradeObservation, ports.ErrAcquisitionUnitAlreadyReserved} {
		if errors.Is(e, v) {
			return v
		}
	}
	return ports.ErrUnavailable
}
func (s *TVEpisodeUpgradeStore) PreviewTVEpisodeUpgrade(ctx context.Context, a *ports.Actor, r ports.TVEpisodeUpgradePreviewRequest) (out *ports.TVEpisodeUpgradeResult, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	defer func() { err = tvUpgradeError(ctx, err) }()
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	m, e := readTVEpisodeUpgradePreview(ctx, tx, a, r, s.decoder)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return m.Result, nil
}

type tvUpgradeAuthority struct {
	OperationID uuid.UUID
	Request     ports.TVEpisodeUpgradePreviewRequest
	ActorID     uuid.UUID
	Version     string
	Snapshot    json.RawMessage
	ValidUntil  time.Time
}
type tvUpgradeSession struct {
	store     *TVEpisodeUpgradeStore
	conn      *PhysicalConnection
	actor     *ports.Actor
	command   ports.TVEpisodeUpgradeCommand
	key, hash string
	lock      int64
	nonce     uuid.UUID
	authority tvUpgradeAuthority
}

func tvUpgradeLock(op uuid.UUID) int64 {
	return int64(binary.BigEndian.Uint64(op[:8])) ^ int64(0x5456455055504752)
}
func (s *TVEpisodeUpgradeStore) CommandTVEpisodeUpgrade(ctx context.Context, a *ports.Actor, c ports.TVEpisodeUpgradeCommand, key string) (out *ports.TVEpisodeUpgradeResult, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	defer func() { err = tvUpgradeError(ctx, err) }()
	if !upgradeKey(key) || len(key) > 128 || !c.Confirmed || c.SubscriptionID == uuid.Nil {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	if c.Action == "reserve" {
		return s.reserveTVUpgrade(ctx, a, c, key)
	}
	if c.OperationID == uuid.Nil {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	work, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	conn, e := AcquirePhysicalConnection(work, s.pool)
	if e != nil {
		return nil, e
	}
	x := &tvUpgradeSession{store: s, conn: conn, actor: a, command: c, key: key, hash: coverageDigest([]any{"tv.episode-upgrade.command.v1", c}), lock: tvUpgradeLock(c.OperationID), nonce: s.ids.NewUUID()}
	defer func() {
		clean, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_, e := conn.Exec(clean, "SELECT pg_advisory_unlock($1),set_config('pulse.tv_upgrade_nonce','',false)", x.lock)
		if e != nil {
			_ = conn.Conn().Close(clean)
		}
		conn.Release()
	}()
	var held bool
	if e = conn.QueryRow(work, "SELECT pg_try_advisory_lock($1)", x.lock).Scan(&held); e != nil {
		return nil, e
	}
	if !held {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	if _, e = conn.Exec(work, "SELECT set_config('pulse.tv_upgrade_nonce',$1,false)", x.nonce.String()); e != nil {
		return nil, e
	}
	var old *ports.TVEpisodeUpgradeResult
	if e = x.transaction(work, false, func(tx pgx.Tx) error { var er error; old, er = x.recover(work, tx); return er }); e != nil {
		return nil, e
	}
	if old != nil {
		return old, nil
	}
	switch c.Action {
	case "prepare":
		return x.prepare(work)
	case "send":
		return x.send(work)
	case "observe":
		return x.observe(work)
	case "organization-preview", "organization-confirm", "organize":
		return x.organization(work)
	case "retirement-preview", "retirement-confirm", "retire":
		return x.retirement(work)
	default:
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
}
func (x *tvUpgradeSession) transaction(ctx context.Context, fresh bool, fn func(pgx.Tx) error) error {
	tx, e := x.conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer upgradeRollback(ctx, tx)
	if e = x.authorize(ctx, tx, fresh); e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
		return e
	}
	if e = x.authorize(ctx, tx, fresh); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (x *tvUpgradeSession) authorize(ctx context.Context, tx pgx.Tx, fresh bool) error {
	_, sub, e := lockUpgradeCommand(ctx, tx, x.actor, x.command.SubscriptionID)
	if e != nil {
		return e
	}
	if e = rejectTVEpisodeUpgradeControl(ctx, tx, x.command.OperationID); e != nil {
		return e
	}
	var r tvUpgradeAuthority
	r.OperationID = x.command.OperationID
	r.Request.SubscriptionID = sub.ID
	row, e := db.New(tx).ReadTVEpisodeUpgradeAuthority(ctx, db.ReadTVEpisodeUpgradeAuthorityParams{OperationID: r.OperationID, SubscriptionID: sub.ID})
	r.ActorID, r.Request.UnitID, r.Request.CurrentFileID, r.Request.CandidateID = row.ActorID, row.UnitID, row.OldFileID, row.CandidateID
	r.Version, r.Snapshot, r.ValidUntil = row.Version, row.Snapshot, row.ValidUntil.Time
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	if e != nil {
		return e
	}
	if r.ActorID.String() != x.actor.ID {
		return ports.ErrForbidden
	}
	x.authority = r
	if x.command.UnitID != uuid.Nil && x.command.UnitID != r.Request.UnitID || x.command.CurrentFileID != uuid.Nil && x.command.CurrentFileID != r.Request.CurrentFileID || x.command.CandidateID != uuid.Nil && x.command.CandidateID != r.Request.CandidateID {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	if (x.command.Action == "prepare" || x.command.Action == "send" || x.command.Action == "observe") && x.command.ExpectedVersion != r.Version {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	var held bool
	e = tx.QueryRow(ctx, `SELECT current_setting('pulse.tv_upgrade_nonce',true)=$2 AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND mode='ExclusiveLock' AND classid=(($1::bigint>>32)&4294967295)::oid AND objid=($1::bigint&4294967295)::oid AND objsubid=1)`, x.lock, x.nonce.String()).Scan(&held)
	if e != nil {
		return e
	}
	if !held {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	if fresh {
		m, e := readTVEpisodeUpgradePreview(ctx, tx, x.actor, r.Request, x.store.decoder)
		if e != nil {
			return e
		}
		if m.Result.Reason != "" || !m.Result.Improvement || m.Result.Version != r.Version {
			return ports.ErrTVEpisodeUpgradeConflict
		}
		if e = tvUpgradeProtection(ctx, tx, r.OperationID, m.Unit.ID, m.Root.ID, x.actor.ID); e != nil {
			return e
		}
		var valid bool
		if e = tx.QueryRow(ctx, "SELECT clock_timestamp()<$1", r.ValidUntil).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return ports.ErrTVEpisodeUpgradeConflict
		}
	}
	return nil
}
func (x *tvUpgradeSession) recover(ctx context.Context, tx pgx.Tx) (*ports.TVEpisodeUpgradeResult, error) {
	actorID, e := uuid.Parse(x.actor.ID)
	if e != nil {
		return nil, ports.ErrForbidden
	}
	receipt, e := db.New(tx).ReadTVEpisodeUpgradePhaseReceipt(ctx, db.ReadTVEpisodeUpgradePhaseReceiptParams{ActorID: actorID, RequestKey: x.key})
	hash, raw := receipt.RequestHash, receipt.Result
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if hash != x.hash {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	var out ports.TVEpisodeUpgradeResult
	if json.Unmarshal(raw, &out) != nil || out.ExecutionAllowed || out.OperationID != x.command.OperationID {
		return nil, ports.ErrTVEpisodeUpgradeConflict
	}
	return &out, nil
}
func (x *tvUpgradeSession) result(status string) *ports.TVEpisodeUpgradeResult {
	return &ports.TVEpisodeUpgradeResult{Status: status, OperationID: x.authority.OperationID, UnitID: x.authority.Request.UnitID, CurrentFileID: x.authority.Request.CurrentFileID, CandidateID: x.authority.Request.CandidateID, Version: x.authority.Version}
}
func (x *tvUpgradeSession) phase(ctx context.Context, tx pgx.Tx, action string, out *ports.TVEpisodeUpgradeResult, evidence any) error {
	raw, e := json.Marshal(out)
	if e != nil {
		return e
	}
	ev, e := json.Marshal(evidence)
	if e != nil {
		return e
	}
	actorID, e := uuid.Parse(x.actor.ID)
	if e != nil {
		return ports.ErrForbidden
	}
	if e = db.New(tx).InsertTVEpisodeUpgradePhase(ctx, db.InsertTVEpisodeUpgradePhaseParams{OperationID: x.authority.OperationID, Action: action, ActorID: actorID, RequestKey: x.key, RequestHash: x.hash, Result: raw, Evidence: ev}); e != nil {
		return e
	}
	return tvUpgradeLedger(ctx, tx, x.store.ids, x.actor, x.authority.OperationID, "tv-episode-upgrade."+action, raw)
}
func tvUpgradeLedger(ctx context.Context, tx pgx.Tx, ids ports.IDGen, a *ports.Actor, op uuid.UUID, action string, raw []byte) error {
	q := db.New(tx)
	if _, e := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: ids.NewUUID(), ActorType: string(a.Type), ActorID: a.ID, Action: action, TargetType: "tv_episode_upgrade", TargetID: op.String(), Payload: raw}); e != nil {
		return e
	}
	_, e := q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: ids.NewUUID(), AggregateType: "tv_episode_upgrade", AggregateID: op.String(), EventType: action, Payload: raw, Seq: 1})
	return e
}
func (x *tvUpgradeSession) evidence(ctx context.Context, tx pgx.Tx, action string, out any) error {
	raw, e := db.New(tx).ReadTVEpisodeUpgradePhaseEvidence(ctx, db.ReadTVEpisodeUpgradePhaseEvidenceParams{OperationID: x.authority.OperationID, Action: action})
	if e != nil {
		return e
	}
	if json.Unmarshal(raw, out) != nil {
		return ports.ErrTVEpisodeUpgradeConflict
	}
	return nil
}
