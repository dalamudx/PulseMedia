package download

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"reflect"
	"strings"
	"sync"
	"time"
)

var _ ports.DownloadControlRepository = (*Repository)(nil)

type controlSnapshot struct {
	task         db.DownloadTask
	client       db.DownloadClient
	stamp        string
	requirements []byte
	authority    postgres.DownloadControlAuthority
}

func controlActor(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return ports.ErrForbidden
	}
	return nil
}
func controlUser(ctx context.Context, q *db.Queries, a *ports.Actor, lock bool) error {
	if e := controlActor(a); e != nil {
		return e
	}
	user, e := q.GetUserByID(ctx, uuid.MustParse(a.ID))
	if lock {
		locked, le := q.LockAcquisitionCommandUser(ctx, uuid.MustParse(a.ID))
		e = le
		user.IsActive = locked.IsActive
		user.Role = locked.Role
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return ports.ErrForbidden
	}
	if e != nil {
		return e
	}
	if !user.IsActive || user.Role != a.Role || (user.Role != ports.RoleAdmin && user.Role != ports.RoleMember) {
		return ports.ErrForbidden
	}
	return nil
}
func controlRequirements(a *ports.Actor, rows []db.DownloadRequirement, shared bool, write bool) error {
	owned := false
	others := false
	for _, r := range rows {
		if r.ActorType == "user" && r.ActorID == a.ID {
			owned = true
		}
		if r.State == "pending" && !r.ReleasedAt.Valid && (r.ActorType != "user" || r.ActorID != a.ID) {
			others = true
		}
	}
	if !owned && a.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	if len(rows) == 0 {
		return ports.ErrNotFound
	}
	if write && others && (a.Role != ports.RoleAdmin || !shared) {
		return ports.ErrForbidden
	}
	if write && shared && a.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return nil
}
func controlReceipt(ctx context.Context, q *db.Queries, c db.DownloadControlCommand) (*ports.DownloadControlReceipt, error) {
	confirmed, e := q.HasDownloadControlConfirmation(ctx, c.ID)
	if e != nil {
		return nil, e
	}
	state := "unknown"
	if confirmed {
		state = "confirmed"
	}
	return &ports.DownloadControlReceipt{CommandID: c.ID, OperationID: c.OperationID, Action: c.Action, State: state}, nil
}
func (r *Repository) GetDownloadControl(ctx context.Context, a *ports.Actor, op uuid.UUID, key string) (*ports.DownloadControlReceipt, error) {
	if e := controlActor(a); e != nil {
		return nil, e
	}
	tx, e := r.execution.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer rollbackControl(tx)
	q := db.New(tx)
	if e = controlUser(ctx, q, a, false); e != nil {
		return nil, e
	}
	rows, e := q.ListRequirementsForOperation(ctx, op)
	if e != nil {
		return nil, e
	}
	rows, e = controlPermissionRows(ctx, tx, a, op, rows)
	if e != nil {
		return nil, e
	}
	if e = controlRequirements(a, rows, false, false); e != nil {
		return nil, e
	}
	command, e := q.GetDownloadControlCommand(ctx, db.GetDownloadControlCommandParams{ActorID: uuid.MustParse(a.ID), RequestKey: key})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if command.OperationID != op {
		return nil, ports.ErrDownloadControlConflict
	}
	receipt, e := controlReceipt(ctx, q, command)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return receipt, nil
}
func rollbackControl(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func (m *Manager) lockControl(ctx context.Context, tx pgx.Tx, a *ports.Actor, op uuid.UUID, shared bool) (controlSnapshot, error) {
	q := db.New(tx)
	var out controlSnapshot
	if e := controlUser(ctx, q, a, true); e != nil {
		return out, e
	}
	task, e := q.GetDownloadTaskByOpID(ctx, op)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ports.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	scope, e := dedicatedControlScope(ctx, tx, op)
	if e != nil {
		return out, e
	}
	var authority postgres.DownloadControlAuthority
	if scope != "" {
		authority, e = postgres.LockDownloadControlAuthority(ctx, tx, a, task, scope, m.sourceConfigurationDecoder())
		if e != nil {
			return out, e
		}
	}
	if _, e = q.LockDownloadTaskUnits(ctx, task.ID); e != nil {
		return out, e
	}
	if e = q.LockDownloadAcquisition(ctx, op); e != nil {
		return out, e
	}
	bound, e := q.GetBoundSubmissionClient(ctx, task.ClientID)
	if e != nil {
		return out, e
	}
	task, e = q.LockDownloadControlTask(ctx, op)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ports.ErrDownloadControlConflict
	}
	if e != nil {
		return out, e
	}
	rows, e := q.LockDownloadControlRequirements(ctx, op)
	if e != nil {
		return out, e
	}
	if scope == "" {
		if e = m.requireNoFrozenUpgrade(ctx, op); e != nil {
			return out, e
		}
		if e = m.requireGenericObservation(ctx, op); e != nil {
			return out, e
		}
	} else if (task.State != "tracking" && (scope != "movie-upgrade" || task.State != "submitting")) || authority.ClientStamp != bound.ConfigStamp {
		return out, ports.ErrDownloadControlConflict
	}
	permissions, e := controlPermissionRows(ctx, tx, a, op, rows)
	if e != nil {
		return out, e
	}
	if e = controlRequirements(a, permissions, shared, true); e != nil {
		return out, e
	}
	if task.State != "tracking" && task.State != "completed" && (scope != "movie-upgrade" || task.State != "submitting") {
		return out, ports.ErrDownloadControlConflict
	}
	hash := strings.ToLower(task.InfoHash)
	_, e = hex.DecodeString(hash)
	if e != nil || (len(hash) != 40 && len(hash) != 64) {
		return out, ports.ErrDownloadControlConflict
	}
	if scope == "" {
		binding, e := q.GetDownloadTaskClientBinding(ctx, task.ID)
		if errors.Is(e, pgx.ErrNoRows) {
			return out, ports.ErrDownloadControlConflict
		}
		if e != nil {
			return out, e
		}
		if binding.ClientID != task.ClientID || binding.ClientStamp != bound.ConfigStamp {
			return out, ports.ErrDownloadControlConflict
		}
	}
	raw, e := json.Marshal(rows)
	if e != nil {
		return out, e
	}
	out = controlSnapshot{task: task, client: bound.DownloadClient, stamp: bound.ConfigStamp, requirements: raw, authority: authority}
	return out, nil
}
func (s controlSnapshot) equal(other controlSnapshot) bool {
	return s.task.ID == other.task.ID && s.task.OperationID == other.task.OperationID && s.task.ClientID == other.task.ClientID && s.task.InfoHash == other.task.InfoHash && s.task.SavePath == other.task.SavePath && s.stamp == other.stamp && bytes.Equal(s.requirements, other.requirements) && s.authority == other.authority
}
func controlIdentity(s controlSnapshot, observed *ports.DownloadPauseObservation) bool {
	if observed == nil || observed.Info == nil {
		return false
	}
	i := observed.Info
	if !strings.EqualFold(s.task.InfoHash, i.InfoHash) || i.SavePath != s.task.SavePath {
		return false
	}
	if i.ContentPath != "" && !sameOrChildPath(s.task.SavePath, i.ContentPath) {
		return false
	}
	tag := "pulsemedia:op:" + s.task.OperationID.String()
	count := 0
	for _, t := range strings.Split(i.Tags, ",") {
		if strings.TrimSpace(t) == tag {
			count++
		}
	}
	return count == 1
}
func (r *Repository) PrepareDownloadControl(ctx context.Context, a *ports.Actor, op uuid.UUID, key string, c ports.DownloadControlCommand, observe bool) (*ports.PreparedDownloadControl, error) {
	if e := controlActor(a); e != nil {
		return nil, e
	}
	actor := *a
	a = &actor
	// Replays restore only the original receipt, even if the original call has not returned.
	old, e := r.GetDownloadControl(ctx, a, op, key)
	if e == nil && !observe {
		command, ce := r.execution.queries.GetDownloadControlCommand(ctx, db.GetDownloadControlCommandParams{ActorID: uuid.MustParse(a.ID), RequestKey: key})
		if ce != nil {
			return nil, ce
		}
		if command.Action != c.Action || command.AffectShared != c.AffectShared {
			return nil, ports.ErrDownloadControlConflict
		}
		return &ports.PreparedDownloadControl{Receipt: old}, nil
	}
	if e != nil && !errors.Is(e, ports.ErrNotFound) {
		return nil, e
	}
	if observe && e != nil {
		return nil, e
	}
	if observe && old.State == "confirmed" {
		return &ports.PreparedDownloadControl{Receipt: old}, nil
	}
	m := r.execution
	task, e := m.queries.GetDownloadTaskByOpID(ctx, op)
	if e != nil {
		return nil, e
	}
	release, e := m.acquireClientBudget(ctx, task.ClientID)
	if e != nil {
		return nil, e
	}
	retained := false
	defer func() {
		if !retained {
			release()
		}
	}()
	conn, e := postgres.AcquirePhysicalConnection(ctx, m.pool)
	if e != nil {
		return nil, e
	}
	gate := false
	var dedicatedRelease func(context.Context)
	var physicalToken uuid.UUID
	cleanup := func() {
		finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if dedicatedRelease != nil {
			dedicatedRelease(finish)
		}
		if gate {
			if ok, err := db.New(conn).UnlockDownloadControlPhysical(finish, task.ID); err != nil || !ok {
				_ = conn.Conn().Close(finish)
			}
		}
		conn.Release()
	}
	defer func() {
		if !retained {
			cleanup()
		}
	}()
	gate, e = db.New(conn).LockDownloadControlPhysical(ctx, task.ID)
	if e != nil {
		return nil, e
	}
	if !gate {
		return nil, ports.ErrDownloadControlConflict
	}
	physicalToken, dedicatedRelease, e = m.acquireDedicatedControl(ctx, conn, op)
	if e != nil {
		return nil, e
	}
	tx, e := conn.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer rollbackControl(tx)
	q := db.New(tx)
	var command db.DownloadControlCommand
	if observe {
		command, e = q.GetDownloadControlCommand(ctx, db.GetDownloadControlCommandParams{ActorID: uuid.MustParse(a.ID), RequestKey: key})
		if e != nil {
			return nil, e
		}
		c = ports.DownloadControlCommand{Action: command.Action, Confirmed: true, AffectShared: command.AffectShared}
	}
	frozen, e := m.lockControl(ctx, tx, a, op, c.AffectShared)
	if e != nil {
		return nil, e
	}
	if observe {
		if command.OperationID != op || command.TaskID != frozen.task.ID || command.ClientStamp != frozen.stamp || command.InfoHash != strings.ToLower(frozen.task.InfoHash) || command.SavePath != frozen.task.SavePath {
			return nil, ports.ErrDownloadControlConflict
		}
		if e = validateControlAuthority(ctx, tx, command.ID, frozen.authority); e != nil {
			return nil, e
		}
		returned, re := q.HasDownloadControlReturned(ctx, command.ID)
		if re != nil {
			return nil, re
		}
		if !returned {
			return &ports.PreparedDownloadControl{Receipt: old}, nil
		}
	} else {
		existing, ce := q.GetDownloadControlCommand(ctx, db.GetDownloadControlCommandParams{ActorID: uuid.MustParse(a.ID), RequestKey: key})
		if ce == nil {
			if existing.OperationID != op || existing.Action != c.Action || existing.AffectShared != c.AffectShared {
				return nil, ports.ErrDownloadControlConflict
			}
			receipt, re := controlReceipt(ctx, q, existing)
			return &ports.PreparedDownloadControl{Receipt: receipt}, re
		}
		if !errors.Is(ce, pgx.ErrNoRows) {
			return nil, ce
		}
		unresolved, ue := q.HasUnresolvedDownloadControl(ctx, frozen.task.ID)
		if ue != nil {
			return nil, ue
		}
		if unresolved {
			return nil, ports.ErrDownloadControlConflict
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	client, e := m.clientForRateSnapshot(ctx, frozen.client)
	if e != nil {
		return nil, e
	}
	native, ok := client.(ports.NativeDownloadControl)
	if !ok {
		return nil, ports.ErrDownloadControlUnsupported
	}
	p := &ports.PreparedDownloadControl{Receipt: old, Client: native, InfoHash: strings.ToLower(frozen.task.InfoHash)}
	var once sync.Once
	p.Release = func() { once.Do(func() { cleanup(); release() }) }
	armed := false
	if !observe {
		p.Arm = func(work context.Context, observed *ports.DownloadPauseObservation) error {
			if armed || !controlIdentity(frozen, observed) {
				return ports.ErrDownloadControlConflict
			}
			armed = true
			tx, e := conn.Begin(work)
			if e != nil {
				return e
			}
			defer rollbackControl(tx)
			q := db.New(tx)
			current, e := m.lockControl(work, tx, a, op, c.AffectShared)
			if e != nil {
				return e
			}
			if !frozen.equal(current) {
				return ports.ErrDownloadControlConflict
			}
			expected := db.CreateDownloadControlCommandParams{ID: m.idgen.NewUUID(), TaskID: frozen.task.ID, OperationID: op, ActorID: uuid.MustParse(a.ID), RequestKey: key, Action: c.Action, AffectShared: c.AffectShared, ClientID: frozen.task.ClientID, ClientStamp: frozen.stamp, InfoHash: strings.ToLower(frozen.task.InfoHash), SavePath: frozen.task.SavePath, Requirements: frozen.requirements, Nonce: m.idgen.NewUUID()}
			command, e = q.CreateDownloadControlCommand(work, expected)
			if e == nil && !sameControlCommand(command, expected) {
				return ports.ErrDownloadControlConflict
			}
			if e != nil {
				return e
			}
			if frozen.authority.Scope != "" {
				_, e = tx.Exec(work, `INSERT INTO download_control_authority(command_id,physical_token,scope,digest) VALUES($1,$2,$3,$4)`, command.ID, physicalToken, frozen.authority.Scope, frozen.authority.Digest)
				if e != nil {
					return e
				}
			}
			if e = m.controlAudit(work, q, a, command, "download.control.authorized"); e != nil {
				return e
			}
			if _, e = tx.Exec(work, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
				return e
			}
			tail, e := m.lockControl(work, tx, a, op, c.AffectShared)
			if e != nil {
				return e
			}
			if !frozen.equal(tail) {
				return ports.ErrDownloadControlConflict
			}
			returned, re := q.HasDownloadControlReturned(work, command.ID)
			if re != nil {
				return re
			}
			if returned {
				return ports.ErrDownloadControlConflict
			}
			valid, e := q.ValidateDownloadControlPermit(work, db.ValidateDownloadControlPermitParams{ID: command.ID, Nonce: command.Nonce})
			if e != nil {
				return e
			}
			if !valid {
				return ports.ErrDownloadControlConflict
			}
			return tx.Commit(work)
		}
	}
	p.Complete = func(work context.Context, nativeErr error, observed *ports.DownloadPauseObservation) (*ports.DownloadControlReceipt, error) {
		if !observe {
			if !armed || command.ID == uuid.Nil {
				return nil, ports.ErrDownloadControlConflict
			}
			// This is release evidence only, never permission to send or publish success.
			tx, e := conn.Begin(work)
			if e != nil {
				return nil, e
			}
			q := db.New(tx)
			e = q.CreateDownloadControlReturn(work, command.ID)
			if e == nil {
				e = m.controlAudit(work, q, a, command, "download.control.returned")
			}
			if e == nil {
				e = tx.Commit(work)
			}
			rollbackControl(tx)
			if e != nil {
				return nil, e
			}
		}
		return m.finishControl(work, conn, a, command, frozen, observe, nativeErr, observed)
	}
	retained = true
	return p, nil
}
func sameControlCommand(c db.DownloadControlCommand, p db.CreateDownloadControlCommandParams) bool {
	var stored, expected any
	left := json.NewDecoder(bytes.NewReader(c.Requirements))
	left.UseNumber()
	right := json.NewDecoder(bytes.NewReader(p.Requirements))
	right.UseNumber()
	if left.Decode(&stored) != nil || right.Decode(&expected) != nil {
		return false
	}
	return c.ID == p.ID && c.TaskID == p.TaskID && c.OperationID == p.OperationID && c.ActorID == p.ActorID && c.RequestKey == p.RequestKey && c.Action == p.Action && c.AffectShared == p.AffectShared && c.ClientID == p.ClientID && c.ClientStamp == p.ClientStamp && c.InfoHash == p.InfoHash && c.SavePath == p.SavePath && c.Nonce == p.Nonce && reflect.DeepEqual(stored, expected)
}

func (m *Manager) controlAudit(ctx context.Context, q *db.Queries, a *ports.Actor, c db.DownloadControlCommand, action string) error {
	raw, _ := json.Marshal(map[string]any{"command_id": c.ID, "operation_id": c.OperationID, "action": c.Action})
	_, e := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: m.idgen.NewUUID(), ActorType: "user", ActorID: a.ID, Action: action, TargetType: "download_task", TargetID: c.TaskID.String(), Payload: raw})
	return e
}
func (m *Manager) finishControl(ctx context.Context, conn *postgres.PhysicalConnection, a *ports.Actor, command db.DownloadControlCommand, frozen controlSnapshot, observe bool, nativeErr error, observed *ports.DownloadPauseObservation) (*ports.DownloadControlReceipt, error) {
	tx, e := conn.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer rollbackControl(tx)
	q := db.New(tx)
	current, e := m.lockControl(ctx, tx, a, command.OperationID, command.AffectShared)
	if e != nil {
		return nil, e
	}
	matches := frozen.equal(current)
	confirmed := nativeErr == nil && matches
	if observe {
		confirmed = confirmed && controlIdentity(frozen, observed) && observed.Paused == (command.Action == "pause")
	}
	if !observe {
		valid, ve := q.ValidateDownloadControlPermit(ctx, db.ValidateDownloadControlPermitParams{ID: command.ID, Nonce: command.Nonce})
		if ve != nil {
			return nil, ve
		}
		confirmed = confirmed && valid
	}
	if confirmed {
		source := "acknowledgement"
		if observe {
			source = "observation"
		}
		if e = q.CreateDownloadControlConfirmation(ctx, db.CreateDownloadControlConfirmationParams{CommandID: command.ID, Source: source}); e != nil {
			return nil, e
		}
		if e = m.controlAudit(ctx, q, a, command, "download.control.confirmed"); e != nil {
			return nil, e
		}
	}
	if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
		return nil, e
	}
	tail, e := m.lockControl(ctx, tx, a, command.OperationID, command.AffectShared)
	if e != nil {
		return nil, e
	}
	if !current.equal(tail) {
		return nil, ports.ErrDownloadControlConflict
	}
	receipt, e := controlReceipt(ctx, q, command)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return receipt, nil
}
