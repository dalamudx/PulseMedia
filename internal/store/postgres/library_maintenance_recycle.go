package postgres

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func maintenanceRecycleItem(ctx context.Context, tx pgx.Tx, rec maintenanceRecycle, raw json.RawMessage) (ports.LibraryRecycleItem, error) {
	status, e := maintenanceRecycleStatus(ctx, tx, rec.ID)
	if e != nil {
		return ports.LibraryRecycleItem{}, e
	}
	item := ports.LibraryRecycleItem{RecycleID: rec.ID, Kind: rec.Kind, OldFileID: rec.OldFileID, ReplacementFileID: rec.ReplacementID, RootID: rec.RootID, OriginalRelativePath: rec.Original, RetainedUntil: rec.Retained, SizeBytes: rec.Identity.Size, Status: status, Version: maintenanceHash(raw)}
	if status == "retained" {
		observation, readErr := db.New(tx).GetLibraryRecycleObservation(ctx, db.GetLibraryRecycleObservationParams{RecycleID: rec.ID, Identity: mustMaintenanceJSON(rec.Identity)})
		if readErr != nil && readErr != pgx.ErrNoRows {
			return item, readErr
		}
		if readErr == nil {
			item.AllocatedBytes = &observation.AllocatedBytes
			item.LinkCount = &observation.LinkCount
			item.UsageObservedAt = &observation.ObservedAt.Time
		}
	}
	return item, nil
}
func mustMaintenanceJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (s *LibraryMaintenanceStore) ListRecycle(ctx context.Context, a *ports.Actor, input ports.LibraryRecycleQuery) (out *ports.LibraryRecycleList, err error) {
	defer func() { err = maintenanceError(err) }()
	input.Query = strings.TrimSpace(input.Query)
	after, e := decodeLibraryRecycleCursor(input)
	if e != nil {
		return nil, e
	}
	limit := input.Limit
	tx, e := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	if _, e = maintenanceUser(ctx, tx, a); e != nil {
		return nil, e
	}
	raws, e := db.New(tx).ListLibraryVerifiedRecycle(ctx, db.ListLibraryVerifiedRecycleParams{AfterID: after, PageLimit: int32(limit + 1), Kind: input.Kind, PathQuery: input.Query})
	if e != nil {
		return nil, e
	}
	out = &ports.LibraryRecycleList{Items: []ports.LibraryRecycleItem{}}
	more := len(raws) > limit
	if more {
		raws = raws[:limit]
	}
	for _, raw := range raws {
		var rec maintenanceRecycle
		if json.Unmarshal(raw, &rec) != nil || rec.OldFileID == uuid.Nil || rec.Identity.Inode < 1 || rec.Retained.IsZero() {
			return nil, ports.ErrUnavailable
		}
		item, e := maintenanceRecycleItem(ctx, tx, rec, raw)
		if e != nil {
			return nil, e
		}
		out.Items = append(out.Items, item)
	}
	if more {
		out.NextCursor = encodeLibraryRecycleCursor(out.Items[len(out.Items)-1].RecycleID, input)
	}
	return out, tx.Commit(ctx)
}
func (s *LibraryMaintenanceStore) PreviewRecycle(ctx context.Context, a *ports.Actor, r ports.LibraryRecyclePreviewRequest) (out *ports.LibraryRecycleItem, err error) {
	defer func() { err = maintenanceError(err) }()
	if (r.Action != "restore" && r.Action != "cleanup") || r.RecycleID == "" || len(r.RecycleID) > 80 {
		return nil, ports.ErrRejected
	}
	c := ports.LibraryMaintenanceCommand{Action: r.Action, RecycleID: r.RecycleID}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	f, state, e := s.material(ctx, tx, a, c, false)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	roots, release, e := s.files.LockRoots(ctx, f.RootPath)
	if e != nil {
		return nil, e
	}
	defer release()
	root := roots[f.RootPath]
	if root == nil {
		return nil, ports.ErrUnavailable
	}
	observation, e := s.inspect(ctx, root, &f, state, false)
	if e != nil {
		return nil, e
	}
	tx, e = s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	next, _, e := s.material(ctx, tx, a, c, true)
	if e != nil {
		return nil, e
	}
	if !maintenanceSameMaterial(f, next) {
		return nil, maintenanceChanged()
	}
	who, e := maintenanceUser(ctx, tx, a)
	if e != nil {
		return nil, e
	}
	if e = db.New(tx).CreateLibraryRecycleObservation(ctx, db.CreateLibraryRecycleObservationParams{ID: s.ids.NewUUID(), RecycleID: r.RecycleID, ActorID: who, Identity: mustMaintenanceJSON(observation.Identity), AllocatedBytes: observation.AllocatedBytes, LinkCount: observation.LinkCount}); e != nil {
		return nil, e
	}
	var rec maintenanceRecycle
	if json.Unmarshal(f.Retirement, &rec) != nil {
		return nil, ports.ErrUnavailable
	}
	item, e := maintenanceRecycleItem(ctx, tx, rec, f.Retirement)
	if e != nil {
		return nil, e
	}
	item.Version = maintenanceHash([]any{"library.maintenance.v1", c.Action, f})
	item.CanConfirm = true
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &item, nil
}
