package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const retirementConfirmAction = "upgrade.retirement-confirm"

type MovieUpgradeRetirementStore struct {
	store      *UpgradePreparationStore
	filesystem ports.RecycleFilesystem
}

func NewMovieUpgradeRetirementStore(s *UpgradePreparationStore, filesystem ports.RecycleFilesystem) (*MovieUpgradeRetirementStore, error) {
	if s == nil || filesystem == nil {
		return nil, preparationConflict()
	}
	return &MovieUpgradeRetirementStore{store: s, filesystem: filesystem}, nil
}

func (r *MovieUpgradeRetirementStore) scoped(sub uuid.UUID) *UpgradePreparationStore {
	s := *r.store
	s.subscriptionScope = sub
	return &s
}

// The current account is locked first, followed by the original business lock
// order. Physical I/O never runs inside this transaction.
func (r *MovieUpgradeRetirementStore) transaction(ctx context.Context, actor *ports.Actor, scope, op uuid.UUID, fn func(context.Context, pgx.Tx, *ports.Actor, uuid.UUID) error) (err error) {
	if e := episodeReviewActor(actor); e != nil {
		return e
	}
	if op == uuid.Nil {
		return preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := r.store.pool.Begin(work)
	if e != nil {
		return e
	}
	defer upgradeRollback(work, tx)
	a, sub, e := r.scoped(scope).authorizePreparation(work, tx, actor, op)
	if e != nil {
		return e
	}
	for _, statement := range []string{
		`SELECT r.id FROM library_root r JOIN movie_upgrade_authority a ON a.root_id=r.id WHERE a.operation_id=$1 FOR NO KEY UPDATE OF r`,
		`SELECT u.id FROM media_unit u JOIN movie_upgrade_authority a ON a.unit_id=u.id WHERE a.operation_id=$1 FOR NO KEY UPDATE OF u`,
		`SELECT m.id FROM media m JOIN media_unit u ON u.media_id=m.id JOIN movie_upgrade_authority a ON a.unit_id=u.id WHERE a.operation_id=$1 FOR NO KEY UPDATE OF m`,
		`SELECT f.id FROM library_file f WHERE f.id IN(SELECT a.old_file_id FROM movie_upgrade_authority a WHERE a.operation_id=$1 UNION SELECT i.file_id FROM movie_upgrade_organization_ingestion i WHERE i.operation_id=$1) ORDER BY f.id FOR UPDATE OF f`,
	} {
		if _, e = tx.Exec(work, statement, op); e != nil {
			return e
		}
	}
	if e = fn(work, tx, a, sub); e != nil {
		return e
	}
	return tx.Commit(work)
}

type retirementBasis struct {
	Authority struct {
		OldFactID uuid.UUID `json:"old_fact_id"`
	} `json:"authority"`
	OldFile struct {
		ID  uuid.UUID `json:"id"`
		Rel string    `json:"rel_path"`
	} `json:"old_file"`
	NewFile struct {
		ID  uuid.UUID `json:"id"`
		Rel string    `json:"rel_path"`
	} `json:"new_file"`
	OldFacts []struct {
		ID      uuid.UUID       `json:"id"`
		Profile quality.Profile `json:"quality_profile"`
	} `json:"old_facts"`
	Ingestion struct {
		Fact struct {
			Profile quality.Profile `json:"quality_profile"`
		} `json:"fact_snapshot"`
	} `json:"ingestion"`
	Revision struct {
		Rules quality.PolicyRules `json:"rules"`
	} `json:"revision"`
	Preparation struct {
		Snapshot struct {
			Physical upgradeOrganizationPhysical `json:"physical"`
		} `json:"snapshot"`
	} `json:"preparation"`
	Root struct {
		Marker string `json:"marker_filename"`
	} `json:"root"`
}

func (r *MovieUpgradeRetirementStore) basis(ctx context.Context, tx pgx.Tx, op uuid.UUID) (raw json.RawMessage, out *ports.MovieUpgradeRetirementPreview, err error) {
	var valid bool
	var stamp string
	var mode ports.UpgradeMode
	e := tx.QueryRow(ctx, `SELECT pulse_upgrade_retirement_state($1),COALESCE(pulse_upgrade_retirement_valid($1),false),encode(sha256(convert_to(jsonb_build_array('upgrade.retirement.v1',pulse_upgrade_retirement_state($1))::text,'UTF8')),'hex'),(SELECT snapshot->>'mode' FROM movie_upgrade_authority WHERE operation_id=$1)`, op).Scan(&raw, &valid, &stamp, &mode)
	if e != nil {
		return nil, nil, e
	}
	if !valid {
		return nil, nil, preparationConflict()
	}
	var b retirementBasis
	if json.Unmarshal(raw, &b) != nil {
		return nil, nil, preparationConflict()
	}
	var current quality.Profile
	found := false
	for _, fact := range b.OldFacts {
		if fact.ID == b.Authority.OldFactID {
			current = fact.Profile
			found = true
		}
	}
	decision := quality.EvaluateUpgradeForMode(b.Revision.Rules, current, b.Ingestion.Fact.Profile, mode)
	if !found || !decision.ScoresKnown || !decision.ShouldUpgrade {
		return nil, nil, preparationConflict()
	}
	return raw, &ports.MovieUpgradeRetirementPreview{OperationID: op, OldFileID: b.OldFile.ID, NewFileID: b.NewFile.ID, OldRelativePath: b.OldFile.Rel, NewRelativePath: b.NewFile.Rel, InputStamp: stamp, CurrentScore: decision.CurrentScore, NewScore: decision.NewScore, RetentionDays: 7}, nil
}

func (r *MovieUpgradeRetirementStore) ReadMovieUpgradeRetirementPreview(ctx context.Context, a *ports.Actor, sub, op uuid.UUID) (out *ports.MovieUpgradeRetirementPreview, err error) {
	if sub == uuid.Nil {
		return nil, preparationConflict()
	}
	err = r.transaction(ctx, a, sub, op, func(work context.Context, tx pgx.Tx, _ *ports.Actor, _ uuid.UUID) error {
		var exists bool
		if e := tx.QueryRow(work, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_retirement_command WHERE operation_id=$1)`, op).Scan(&exists); e != nil {
			return e
		}
		if exists {
			return preparationConflict()
		}
		_, v, e := r.basis(work, tx, op)
		out = v
		return e
	})
	return
}

type retirementCommand struct {
	OperationID uuid.UUID       `json:"operation_id"`
	CommandID   uuid.UUID       `json:"command_id"`
	ActorID     uuid.UUID       `json:"actor_id"`
	RequestKey  string          `json:"request_key"`
	Stamp       string          `json:"input_stamp"`
	Baseline    json.RawMessage `json:"baseline"`
	Recycle     string          `json:"recycle_rel"`
	At          time.Time       `json:"commanded_at"`
}

func (c retirementCommand) receipt() *ports.MovieUpgradeRetirementReceipt {
	return &ports.MovieUpgradeRetirementReceipt{OperationID: c.OperationID, CommandID: c.CommandID, QueuedAt: c.At, Status: "retirement_queued"}
}
func (c retirementCommand) input() ports.ConfirmMovieUpgradeRetirement {
	return ports.ConfirmMovieUpgradeRetirement{OperationID: c.OperationID, InputStamp: c.Stamp, Confirmed: true}
}

func (r *MovieUpgradeRetirementStore) recover(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub, op uuid.UUID, key string, supplied *ports.ConfirmMovieUpgradeRetirement) (*ports.MovieUpgradeRetirementReceipt, error) {
	var raw json.RawMessage
	e := tx.QueryRow(ctx, `SELECT to_jsonb(c) FROM movie_upgrade_retirement_command c WHERE operation_id=$1 AND actor_id=$2 AND request_key=$3`, op, a.ID, retirementConfirmAction+":"+key).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var c retirementCommand
	if json.Unmarshal(raw, &c) != nil || c.CommandID == uuid.Nil {
		return nil, preparationConflict()
	}
	if supplied != nil && *supplied != c.input() {
		return nil, preparationConflict()
	}
	out := c.receipt()
	digest := coverageDigest([]any{"upgrade.retirement-confirm.v1", c.input()})
	own, e := readUpgradeObservationLedger(ctx, tx, op, a, sub, key, digest, retirementConfirmAction, out, true)
	if e != nil {
		return nil, e
	}
	if e = own.verify(ctx, tx); e != nil {
		return nil, e
	}
	return out, nil
}
func (r *MovieUpgradeRetirementStore) ReadMovieUpgradeRetirementReceipt(ctx context.Context, a *ports.Actor, sub, op uuid.UUID, key string) (out *ports.MovieUpgradeRetirementReceipt, err error) {
	if sub == uuid.Nil || !upgradeKey(key) || len(key) > 100 {
		return nil, preparationConflict()
	}
	err = r.transaction(ctx, a, sub, op, func(work context.Context, tx pgx.Tx, actor *ports.Actor, s uuid.UUID) error {
		var e error
		out, e = r.recover(work, tx, actor, s, op, key, nil)
		return e
	})
	return
}
func (r *MovieUpgradeRetirementStore) ConfirmMovieUpgradeRetirement(ctx context.Context, a *ports.Actor, sub uuid.UUID, key string, input ports.ConfirmMovieUpgradeRetirement) (out *ports.MovieUpgradeRetirementReceipt, err error) {
	if sub == uuid.Nil || !input.Confirmed || len(input.InputStamp) != 64 || !upgradeKey(key) || len(key) > 100 {
		return nil, preparationConflict()
	}
	err = r.transaction(ctx, a, sub, input.OperationID, func(work context.Context, tx pgx.Tx, actor *ports.Actor, s uuid.UUID) error {
		var e error
		out, e = r.recover(work, tx, actor, s, input.OperationID, key, &input)
		if e != nil || out != nil {
			return e
		}
		baseline, preview, e := r.basis(work, tx, input.OperationID)
		if e != nil {
			return e
		}
		if preview.InputStamp != input.InputStamp {
			return preparationConflict()
		}
		id := r.store.ids.NewUUID()
		var raw json.RawMessage
		e = tx.QueryRow(work, `INSERT INTO movie_upgrade_retirement_command(operation_id,command_id,actor_id,request_key,input_stamp,baseline,recycle_rel) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING to_jsonb(movie_upgrade_retirement_command)`, input.OperationID, id, actor.ID, retirementConfirmAction+":"+key, input.InputStamp, baseline, ".pulsemedia_recycle/"+input.OperationID.String()+"/"+preview.OldRelativePath).Scan(&raw)
		if e != nil {
			return e
		}
		var c retirementCommand
		if json.Unmarshal(raw, &c) != nil {
			return preparationConflict()
		}
		out = c.receipt()
		digest := coverageDigest([]any{"upgrade.retirement-confirm.v1", input})
		own, e := readUpgradeObservationLedger(work, tx, input.OperationID, actor, s, key, digest, retirementConfirmAction, out, false)
		if e != nil {
			return e
		}
		check := func() error {
			var same bool
			if e := tx.QueryRow(work, `SELECT to_jsonb(c)=$2::jsonb AND c.baseline=pulse_upgrade_retirement_state(c.operation_id) AND pulse_upgrade_retirement_valid(c.operation_id) FROM movie_upgrade_retirement_command c WHERE operation_id=$1`, input.OperationID, raw).Scan(&same); e != nil {
				return e
			}
			if !same {
				return preparationConflict()
			}
			return own.verify(work, tx)
		}
		ledger := UpgradeReservationStore{pool: r.store.pool, ids: r.store.ids}
		if e = ledger.upgradeSuccess(work, tx, actor, s, retirementConfirmAction+":"+key, digest, retirementConfirmAction, input.OperationID, out, own, check); e != nil {
			return e
		}
		if _, e = r.recover(work, tx, actor, s, input.OperationID, key, &input); e != nil {
			return e
		}
		return check()
	})
	return
}

func (r *MovieUpgradeRetirementStore) PendingMovieUpgradeRetirements(ctx context.Context, limit int) ([]ports.MovieUpgradeRetirementWork, error) {
	if limit < 1 || limit > 128 {
		return nil, preparationConflict()
	}
	rows, e := r.store.pool.Query(ctx, `SELECT c.operation_id,c.actor_id FROM movie_upgrade_retirement_command c JOIN app_user a ON a.id=c.actor_id LEFT JOIN movie_upgrade_retirement_permit p USING(operation_id) LEFT JOIN movie_upgrade_retirement_result r USING(operation_id) WHERE r.operation_id IS NULL AND a.is_active AND a.role='admin' AND (p.operation_id IS NULL OR p.lease_until<=clock_timestamp()) ORDER BY c.commanded_at,c.operation_id LIMIT $1`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := make([]ports.MovieUpgradeRetirementWork, 0)
	for rows.Next() {
		var v ports.MovieUpgradeRetirementWork
		if e = rows.Scan(&v.OperationID, &v.ActorID); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *MovieUpgradeRetirementStore) record(ctx context.Context, tx pgx.Tx, a *ports.Actor, sub, op uuid.UUID, action string, raw json.RawMessage, check func() error) error {
	key := action + ":" + op.String()
	digest := coverageDigest([]any{action + ".v1", raw})
	own, e := readUpgradeObservationLedger(ctx, tx, op, a, sub, key, digest, action, raw, false)
	if e != nil {
		return e
	}
	verify := func() error {
		if e := check(); e != nil {
			return e
		}
		return own.verify(ctx, tx)
	}
	ledger := UpgradeReservationStore{pool: r.store.pool, ids: r.store.ids}
	return ledger.upgradeSuccess(ctx, tx, a, sub, action+":"+key, digest, action, op, raw, own, verify)
}

func (r *MovieUpgradeRetirementStore) ExecuteMovieUpgradeRetirement(ctx context.Context, a *ports.Actor, op uuid.UUID) error {
	// Obtain the configured root without granting a physical attempt. Retain its
	// cross-process slot through the call, observation and all writebacks.
	var command retirementCommand
	var b retirementBasis
	e := r.transaction(ctx, a, uuid.Nil, op, func(work context.Context, tx pgx.Tx, _ *ports.Actor, _ uuid.UUID) error {
		var raw json.RawMessage
		if e := tx.QueryRow(work, `SELECT to_jsonb(c) FROM movie_upgrade_retirement_command c WHERE operation_id=$1`, op).Scan(&raw); e != nil {
			return e
		}
		if json.Unmarshal(raw, &command) != nil || json.Unmarshal(command.Baseline, &b) != nil {
			return preparationConflict()
		}
		return nil
	})
	if e != nil {
		return e
	}
	physical := b.Preparation.Snapshot.Physical
	roots, release, e := r.filesystem.LockRoots(ctx, physical.RootPath)
	if e != nil {
		return e
	}
	defer release()
	root := roots[physical.RootPath]
	if root == nil {
		return preparationConflict()
	}
	physicalValid := retirementInspect(ctx, root, b, command.Recycle, false) == nil
	var permit uuid.UUID
	attempt := false
	done := false
	e = r.transaction(ctx, a, uuid.Nil, op, func(work context.Context, tx pgx.Tx, actor *ports.Actor, sub uuid.UUID) error {
		var exists bool
		if e := tx.QueryRow(work, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_retirement_result WHERE operation_id=$1)`, op).Scan(&exists); e != nil {
			return e
		}
		if exists {
			done = true
			return nil
		}
		var existing uuid.UUID
		e := tx.QueryRow(work, `SELECT permit_id FROM movie_upgrade_retirement_permit WHERE operation_id=$1`, op).Scan(&existing)
		if e == nil {
			permit = existing
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		baseline, _, e := r.basis(work, tx, op)
		if e != nil || !preparationSameJSON(baseline, command.Baseline) {
			return nil
		}
		if !physicalValid {
			return nil
		}
		permit = r.store.ids.NewUUID()
		owner := r.store.ids.NewUUID()
		var raw json.RawMessage
		e = tx.QueryRow(work, `WITH n AS(SELECT clock_timestamp() t) INSERT INTO movie_upgrade_retirement_permit SELECT $1,$2,$3,1,t,t+interval '60 seconds' FROM n RETURNING to_jsonb(movie_upgrade_retirement_permit)`, op, permit, owner).Scan(&raw)
		if e != nil {
			return e
		}
		check := func() error {
			var same bool
			if e := tx.QueryRow(work, `SELECT to_jsonb(p)=$2::jsonb AND p.lease_until>clock_timestamp() AND c.baseline=pulse_upgrade_retirement_state(c.operation_id) FROM movie_upgrade_retirement_permit p JOIN movie_upgrade_retirement_command c USING(operation_id) WHERE p.operation_id=$1`, op, raw).Scan(&same); e != nil {
				return e
			}
			if !same {
				return preparationConflict()
			}
			return nil
		}
		if e = r.record(work, tx, actor, sub, op, "upgrade.retirement-permit", raw, check); e != nil {
			return e
		}
		attempt = true
		return check()
	})
	if e != nil || done {
		return e
	}
	trusted := false
	if attempt {
		e = r.filesystem.Recycle(ctx, root, physical.OldRel, command.Recycle, physical.Old)
		// Cancellation and uncertain filesystem returns cannot retire any fact.
		trusted = e == nil && ctx.Err() == nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	var observed time.Time
	if e = r.store.pool.QueryRow(cleanup, `SELECT clock_timestamp()`).Scan(&observed); e != nil {
		return e
	}
	if trusted {
		trusted = retirementInspect(cleanup, root, b, command.Recycle, true) == nil
	}
	return r.finish(cleanup, a, op, permit, observed, trusted)
}

func retirementInspect(ctx context.Context, root ports.TransferRoot, b retirementBasis, recycle string, moved bool) error {
	p := b.Preparation.Snapshot.Physical
	identity, e := root.Identity(ctx)
	if e != nil {
		return e
	}
	if identity.Device != p.Root.Device || identity.Inode != p.Root.Inode {
		return ports.ErrFileIdentity
	}
	marker := b.Root.Marker
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	m, e := root.Inspect(ctx, marker)
	if e != nil || m.Size <= 0 {
		return ports.ErrFileIdentity
	}
	target, e := root.Inspect(ctx, p.TargetRel)
	if e != nil || target != p.Source {
		return ports.ErrFileIdentity
	}
	oldPath := p.OldRel
	if moved {
		oldPath = recycle
	}
	old, e := root.Inspect(ctx, oldPath)
	if e != nil || old != p.Old {
		return ports.ErrFileIdentity
	}
	absent := recycle
	if moved {
		absent = p.OldRel
	}
	if _, e = root.Inspect(ctx, absent); !errors.Is(e, fs.ErrNotExist) {
		return ports.ErrFileIdentity
	}
	return nil
}

func (r *MovieUpgradeRetirementStore) finish(ctx context.Context, a *ports.Actor, op, permit uuid.UUID, observed time.Time, trusted bool) error {
	return r.transaction(ctx, a, uuid.Nil, op, func(work context.Context, tx pgx.Tx, actor *ports.Actor, sub uuid.UUID) error {
		var exists bool
		if e := tx.QueryRow(work, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_retirement_result WHERE operation_id=$1)`, op).Scan(&exists); e != nil {
			return e
		}
		if exists {
			return nil
		}
		var baseline json.RawMessage
		var valid bool
		if e := tx.QueryRow(work, `SELECT pulse_upgrade_retirement_state($1),COALESCE(pulse_upgrade_retirement_valid($1),false) AND EXISTS(SELECT 1 FROM movie_upgrade_retirement_command c JOIN movie_upgrade_retirement_permit p USING(operation_id) WHERE c.operation_id=$1 AND p.permit_id=$2 AND p.lease_until>clock_timestamp() AND c.baseline=pulse_upgrade_retirement_state($1))`, op, permit).Scan(&baseline, &valid); e != nil {
			return e
		}
		status := "manual_review"
		if trusted && valid {
			status = "retired"
		}
		var raw json.RawMessage
		var permitParam any
		if permit != uuid.Nil {
			permitParam = permit
		}
		if e := tx.QueryRow(work, `WITH n AS(SELECT clock_timestamp() t) INSERT INTO movie_upgrade_retirement_result SELECT $1,$2,$3,$4,$5,t,CASE WHEN $3='retired' THEN t+interval '7 days' END FROM n RETURNING to_jsonb(movie_upgrade_retirement_result)`, op, permitParam, status, baseline, observed).Scan(&raw); e != nil {
			return e
		}
		if status == "retired" {
			var recorded time.Time
			var v struct {
				At time.Time `json:"recorded_at"`
			}
			if json.Unmarshal(raw, &v) != nil {
				return preparationConflict()
			}
			recorded = v.At
			for _, statement := range []string{
				`UPDATE library_file f SET retired_at=$2,last_mutation_seq=root.mutation_seq+1 FROM movie_upgrade_authority a JOIN library_root root ON root.id=a.root_id WHERE a.operation_id=$1 AND f.id=a.old_file_id`,
				`UPDATE unit_fact SET revoked_at=$2,revocation_reason='quality_upgrade' WHERE source_file_id=(SELECT old_file_id FROM movie_upgrade_authority WHERE operation_id=$1) AND revoked_at IS NULL`,
				`UPDATE acquisition_reservation SET released_at=$2,updated_at=$2 WHERE operation_id=$1`,
				`UPDATE unit_reservation SET state='consumed',updated_at=$2 WHERE operation_id=$1`,
				`UPDATE download_requirement SET state='imported' WHERE operation_id=$1 AND $2::timestamptz IS NOT NULL`,
				`UPDATE library_root SET mutation_seq=mutation_seq+1,updated_at=$2 WHERE id=(SELECT root_id FROM movie_upgrade_authority WHERE operation_id=$1)`,
			} {
				if _, e := tx.Exec(work, statement, op, recorded); e != nil {
					return e
				}
			}
		}
		check := func() error {
			var same bool
			if e := tx.QueryRow(work, `SELECT to_jsonb(r)=$2::jsonb FROM movie_upgrade_retirement_result r WHERE operation_id=$1`, op, raw).Scan(&same); e != nil {
				return e
			}
			if !same {
				return preparationConflict()
			}
			_, e := tx.Exec(work, `SELECT pulse_assert_upgrade_retirement_result($1)`, op)
			return e
		}
		if e := r.record(work, tx, actor, sub, op, "upgrade.retirement-result", raw, check); e != nil {
			return e
		}
		return check()
	})
}

var _ ports.MovieUpgradeRetirementRepository = (*MovieUpgradeRetirementStore)(nil)
var _ ports.MovieUpgradeRetirementRuntime = (*MovieUpgradeRetirementStore)(nil)
