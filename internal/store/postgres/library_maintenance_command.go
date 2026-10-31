package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type maintenanceCommandRow struct {
	ID      uuid.UUID                       `json:"id"`
	ActorID uuid.UUID                       `json:"actor_id"`
	Action  string                          `json:"action"`
	Hash    string                          `json:"request_hash"`
	Input   ports.LibraryMaintenanceCommand `json:"input"`
	Frozen  maintenanceFrozen               `json:"frozen"`
	FileID  uuid.UUID                       `json:"file_id"`
}
type maintenancePermit struct{ Owner, Token, Nonce uuid.UUID }

func maintenanceRead(ctx context.Context, tx pgx.Tx, actor uuid.UUID, key string, input *ports.LibraryMaintenanceCommand) (*ports.LibraryMaintenanceResult, error) {
	stored, e := db.New(tx).GetLibraryMaintenanceCommand(ctx, db.GetLibraryMaintenanceCommandParams{ActorID: actor, RequestKey: key})
	raw, result := stored.Command, stored.Result
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var row maintenanceCommandRow
	if json.Unmarshal(raw, &row) != nil {
		return nil, maintenanceChanged()
	}
	if input != nil && row.Hash != maintenanceHash(input) {
		return nil, maintenanceChanged()
	}
	if len(result) > 0 {
		var out ports.LibraryMaintenanceResult
		if json.Unmarshal(result, &out) != nil || out.CommandID != row.ID || out.ExecutionAllowed {
			return nil, maintenanceChanged()
		}
		return &out, nil
	}
	return &ports.LibraryMaintenanceResult{CommandID: row.ID, Action: row.Action, Status: "unknown_requires_review", FileID: row.FileID, Version: row.Input.ExpectedVersion}, nil
}
func (s *LibraryMaintenanceStore) ReadCommand(ctx context.Context, a *ports.Actor, key string) (out *ports.LibraryMaintenanceResult, err error) {
	defer func() { err = maintenanceError(err) }()
	if !maintenanceKey(key) {
		return nil, ports.ErrRejected
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	who, e := maintenanceUser(ctx, tx, a)
	if e != nil {
		return nil, e
	}
	out, e = maintenanceRead(ctx, tx, who, key, nil)
	if e != nil {
		return nil, e
	}
	if out == nil {
		return nil, ports.ErrNotFound
	}
	return out, tx.Commit(ctx)
}
func maintenanceCommandValid(c ports.LibraryMaintenanceCommand) bool {
	if !c.Confirmed || len(c.ExpectedVersion) != 64 {
		return false
	}
	if _, e := fmt.Sscanf(c.ExpectedVersion, "%64x", new([]byte)); e != nil {
		return false
	}
	switch c.Action {
	case "reorganize":
		return c.FileID != uuid.Nil && c.RecycleID == "" && maintenancePath(c.TargetRelativePath)
	case "restore", "cleanup":
		return c.FileID == uuid.Nil && c.TargetRelativePath == "" && len(c.RecycleID) > 0 && len(c.RecycleID) < 80
	}
	return false
}
func (s *LibraryMaintenanceStore) PrepareCommand(ctx context.Context, a *ports.Actor, c ports.LibraryMaintenanceCommand, key string) (prepared ports.PreparedLibraryMaintenance, err error) {
	defer func() { err = maintenanceError(err) }()
	if _, e := maintenanceActor(a); e != nil {
		return nil, e
	}
	if !maintenanceKey(key) || !maintenanceCommandValid(c) {
		return nil, ports.ErrRejected
	}
	conn, e := AcquirePhysicalConnection(ctx, s.pool)
	if e != nil {
		return nil, e
	}
	adopted := false
	defer func() {
		if !adopted {
			conn.Release()
		}
	}()
	tx, e := conn.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	who, e := maintenanceUser(ctx, tx, a)
	if e != nil {
		return nil, e
	}
	previous, e := maintenanceRead(ctx, tx, who, key, &c)
	if e != nil {
		return nil, e
	}
	if previous != nil {
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return &maintenanceExecution{previous: previous}, nil
	}
	frozen, state, e := s.material(ctx, tx, a, c, false)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	roots, release, e := s.files.LockRoots(ctx, frozen.RootPath)
	if e != nil {
		return nil, e
	}
	defer func() {
		if !adopted {
			release()
		}
	}()
	root := roots[frozen.RootPath]
	if root == nil {
		return nil, ports.ErrUnavailable
	}
	// Recover requests that completed while this caller waited for the root.
	tx, e = conn.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	if _, e = maintenanceUser(ctx, tx, a); e != nil {
		return nil, e
	}
	previous, e = maintenanceRead(ctx, tx, who, key, &c)
	if e != nil {
		return nil, e
	}
	if previous != nil {
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return &maintenanceExecution{previous: previous}, nil
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	if _, e = s.inspect(ctx, root, &frozen, state, false); e != nil {
		return nil, e
	}
	version := maintenanceHash([]any{"library.maintenance.v1", c.Action, frozen})
	if version != c.ExpectedVersion {
		return nil, maintenanceChanged()
	}
	permit := maintenancePermit{Owner: s.ids.NewUUID(), Token: s.ids.NewUUID(), Nonce: s.ids.NewUUID()}
	id := s.ids.NewUUID()
	tx, e = conn.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	if _, e = maintenanceUser(ctx, tx, a); e != nil {
		return nil, e
	}
	previous, e = maintenanceRead(ctx, tx, who, key, &c)
	if e != nil {
		return nil, e
	}
	if previous != nil {
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return &maintenanceExecution{previous: previous}, nil
	}
	current, _, e := s.material(ctx, tx, a, c, true)
	if e != nil {
		return nil, e
	}
	if !maintenanceSameMaterial(frozen, current) {
		return nil, maintenanceChanged()
	}
	if e = db.New(tx).SetLibraryMaintenanceNonce(ctx, permit.Nonce.String()); e != nil {
		return nil, e
	}
	inputRaw, _ := json.Marshal(c)
	frozenRaw, _ := json.Marshal(frozen)
	if e = db.New(tx).CreateLibraryMaintenanceCommand(ctx, db.CreateLibraryMaintenanceCommandParams{ID: id, ActorID: who, RequestKey: key, Action: c.Action, Input: inputRaw, RequestHash: maintenanceHash(&c), RootID: state.Root.ID, FileID: state.File.ID, RecycleID: c.RecycleID, Frozen: frozenRaw}); e != nil {
		return nil, e
	}
	if e = db.New(tx).CreateLibraryMaintenancePermit(ctx, db.CreateLibraryMaintenancePermitParams{CommandID: id, Owner: permit.Owner, Token: permit.Token, Nonce: permit.Nonce}); e != nil {
		return nil, e
	}
	if e = db.New(tx).CheckLibraryMaintenanceConstraints(ctx); e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	adopted = true
	return &maintenanceExecution{store: s, conn: conn, actor: a, command: c, id: id, permit: permit, frozen: frozen, state: state, root: root, release: release, result: &ports.LibraryMaintenanceResult{CommandID: id, Action: c.Action, Status: "unknown_requires_review", FileID: state.File.ID, Version: version}}, nil
}

func (s *LibraryMaintenanceStore) maintenanceBeforeCall(ctx context.Context, conn *PhysicalConnection, a *ports.Actor, c ports.LibraryMaintenanceCommand, id uuid.UUID, p maintenancePermit, f maintenanceFrozen) error {
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	defer upgradeRollback(ctx, tx)
	current, _, e := s.materialIgnoringOwnCommand(ctx, tx, a, c, id, true)
	if e != nil {
		return e
	}
	if !maintenanceSameMaterial(f, current) {
		return maintenanceChanged()
	}
	if e = maintenancePermitValid(ctx, tx, id, p); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func maintenancePermitValid(ctx context.Context, tx pgx.Tx, id uuid.UUID, p maintenancePermit) error {
	ok, e := db.New(tx).ValidateLibraryMaintenancePermit(ctx, db.ValidateLibraryMaintenancePermitParams{CommandID: id, Owner: p.Owner, Token: p.Token, Nonce: p.Nonce})
	if e != nil {
		return e
	}
	if !ok {
		return maintenanceChanged()
	}
	return nil
}

// The current command is already immutable. Its own pending return is not a
// different operation and is excluded only when checking the recycle status.
func (s *LibraryMaintenanceStore) materialIgnoringOwnCommand(ctx context.Context, tx pgx.Tx, a *ports.Actor, c ports.LibraryMaintenanceCommand, id uuid.UUID, lock bool) (maintenanceFrozen, maintenanceSnapshot, error) {
	if e := db.New(tx).SetLibraryMaintenanceCurrentCommand(ctx, id.String()); e != nil {
		return maintenanceFrozen{}, maintenanceSnapshot{}, e
	}
	return s.material(ctx, tx, a, c, lock)
}
func (s *LibraryMaintenanceStore) maintenanceFinish(ctx context.Context, conn *PhysicalConnection, a *ports.Actor, c ports.LibraryMaintenanceCommand, id uuid.UUID, p maintenancePermit, f maintenanceFrozen, state maintenanceSnapshot, observed time.Time, trusted bool, out *ports.LibraryMaintenanceResult) (*ports.LibraryMaintenanceResult, error) {
	tx, e := conn.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer upgradeRollback(ctx, tx)
	result := *out
	var expected json.RawMessage
	var restoredFact uuid.UUID
	var restoredProfile json.RawMessage
	if trusted {
		current, _, e := s.materialIgnoringOwnCommand(ctx, tx, a, c, id, true)
		if e != nil {
			return out, e
		}
		if !maintenanceSameMaterial(f, current) {
			return out, maintenanceChanged()
		}
		if e = maintenancePermitValid(ctx, tx, id, p); e != nil {
			return out, e
		}
		at, e := maintenanceDBTime(ctx, tx)
		if e != nil {
			return out, e
		}
		if at.Before(observed) || at.Sub(observed) > 5*time.Second {
			return out, maintenanceChanged()
		}
		switch c.Action {
		case "reorganize":
			affected, e := db.New(tx).ReorganizeLibraryMaintenanceFile(ctx, db.ReorganizeLibraryMaintenanceFileParams{FileID: state.File.ID, Target: f.Target, RootID: state.Root.ID})
			if e != nil {
				return out, e
			}
			if affected != 1 {
				return out, maintenanceChanged()
			}
		case "restore":
			fresh := s.ids.NewUUID()
			restoredFact = s.ids.NewUUID()
			profile := json.RawMessage(nil)
			for _, fact := range state.Facts {
				if fact.UnitID == state.Units[0].ID && (fact.Stage == "imported" || fact.Stage == "confirmed") && maintenanceKnownQuality(fact.Profile) {
					if profile != nil && !preparationSameJSON(profile, fact.Profile) {
						return out, maintenanceChanged()
					}
					profile = fact.Profile
				}
			}
			if profile == nil {
				return out, maintenanceChanged()
			}
			if e = db.New(tx).RestoreLibraryMaintenanceFile(ctx, db.RestoreLibraryMaintenanceFileParams{FileID: fresh, RootID: state.Root.ID, Target: f.Target, SizeBytes: f.Identity.Size, MtimeNs: f.Identity.Mtime, Fingerprint: state.File.Fingerprint, At: pgtype.Timestamptz{Time: at, Valid: true}}); e != nil {
				return out, e
			}
			if e = db.New(tx).RestoreLibraryMaintenanceMapping(ctx, db.RestoreLibraryMaintenanceMappingParams{FileID: fresh, UnitID: state.Units[0].ID, At: pgtype.Timestamptz{Time: at, Valid: true}}); e != nil {
				return out, e
			}
			if e = db.New(tx).RestoreLibraryMaintenanceFact(ctx, db.RestoreLibraryMaintenanceFactParams{FactID: restoredFact, UnitID: state.Units[0].ID, FileID: fresh, RootID: state.Root.ID, Profile: profile, At: pgtype.Timestamptz{Time: at, Valid: true}}); e != nil {
				return out, e
			}
			result.FileID = fresh
			restoredProfile = profile
		}
		if e = db.New(tx).AdvanceLibraryMaintenanceRoot(ctx, db.AdvanceLibraryMaintenanceRootParams{RootID: state.Root.ID, At: pgtype.Timestamptz{Time: at, Valid: true}}); e != nil {
			return out, e
		}
		result.Status = "completed"
		expected, e = maintenanceExpectedAfter(ctx, tx, f, c, result, at, restoredFact, restoredProfile)
		if e != nil {
			return out, e
		}
	}
	if !trusted {
		other := uuid.Nil
		if f.ReplacementID != nil {
			other = *f.ReplacementID
		}
		if e = maintenanceLock(ctx, tx, state, other); e != nil {
			return out, e
		}
		if expected, e = db.New(tx).GetLibraryMaintenanceUnknownAfterState(ctx, db.GetLibraryMaintenanceUnknownAfterStateParams{FileID: state.File.ID, ReplacementID: searchPGUUIDValue(f.ReplacementID)}); e != nil {
			return out, e
		}
	}
	raw, _ := json.Marshal(result)
	auditID, eventID := s.ids.NewUUID(), s.ids.NewUUID()
	if e = db.New(tx).CreateLibraryMaintenanceAudit(ctx, db.CreateLibraryMaintenanceAuditParams{ID: auditID, ActorID: a.ID, Action: "library.maintenance." + c.Action, TargetID: id.String(), Payload: raw}); e != nil {
		return out, e
	}
	if e = db.New(tx).CreateLibraryMaintenanceEvent(ctx, db.CreateLibraryMaintenanceEventParams{ID: eventID, AggregateID: id.String(), EventType: "library.maintenance." + c.Action, Payload: raw}); e != nil {
		return out, e
	}
	var after json.RawMessage
	if after, e = db.New(tx).GetLibraryMaintenanceAfterState(ctx, db.GetLibraryMaintenanceAfterStateParams{FileID: state.File.ID, ReplacementID: searchPGUUIDValue(f.ReplacementID), Restored: c.Action == "restore" && trusted, RestoredFileID: result.FileID}); e != nil {
		return out, e
	}
	if !preparationSameJSON(after, expected) {
		return out, maintenanceChanged()
	}
	if e = db.New(tx).CreateLibraryMaintenanceReturn(ctx, db.CreateLibraryMaintenanceReturnParams{CommandID: id, Owner: p.Owner, Token: p.Token, Status: result.Status, Result: raw, AfterState: after, AuditID: auditID, EventID: eventID}); e != nil {
		return out, e
	}
	if e = db.New(tx).CheckLibraryMaintenanceConstraints(ctx); e != nil {
		return out, e
	}
	if trusted {
		if e = maintenancePermitValid(ctx, tx, id, p); e != nil {
			return out, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	return &result, nil
}
