package postgres

import (
	"context"
	"sync"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type maintenanceExecution struct {
	store    *LibraryMaintenanceStore
	conn     *PhysicalConnection
	actor    *ports.Actor
	command  ports.LibraryMaintenanceCommand
	id       uuid.UUID
	permit   maintenancePermit
	frozen   maintenanceFrozen
	state    maintenanceSnapshot
	root     ports.TransferRoot
	release  func()
	previous *ports.LibraryMaintenanceResult
	result   *ports.LibraryMaintenanceResult
	once     sync.Once
	callOnce sync.Once
}

func (p *maintenanceExecution) PreviousResult() *ports.LibraryMaintenanceResult { return p.previous }
func (p *maintenanceExecution) Close() {
	p.once.Do(func() {
		if p.release != nil {
			p.release()
		}
		if p.conn != nil {
			p.conn.Release()
		}
	})
}
func (p *maintenanceExecution) BeforeCall(ctx context.Context) error {
	if _, err := p.store.inspect(ctx, p.root, &p.frozen, p.state, false); err != nil {
		return maintenanceError(err)
	}
	return maintenanceError(p.store.maintenanceBeforeCall(ctx, p.conn, p.actor, p.command, p.id, p.permit, p.frozen))
}
func (p *maintenanceExecution) Remove(ctx context.Context) error {
	err := ports.ErrRejected
	p.callOnce.Do(func() { err = p.store.files.MaintenanceRemove(ctx, p.root, p.frozen.Source, p.frozen.Identity) })
	return err
}
func (p *maintenanceExecution) Rename(ctx context.Context) error {
	err := ports.ErrRejected
	p.callOnce.Do(func() {
		err = p.store.files.MaintenanceRename(ctx, p.root, p.frozen.Source, p.frozen.Target, p.frozen.Identity)
	})
	return err
}
func (p *maintenanceExecution) Observe(ctx context.Context) error {
	if p.command.Action == "cleanup" {
		if !p.store.maintenanceCleanupObserved(ctx, p.root, p.frozen, p.state) {
			return ports.ErrUnavailable
		}
		return nil
	}
	_, err := p.store.inspect(ctx, p.root, &p.frozen, p.state, true)
	return maintenanceError(err)
}
func (p *maintenanceExecution) Complete(ctx context.Context, trusted bool) (*ports.LibraryMaintenanceResult, error) {
	stamp, err := db.New(p.conn).GetLibraryMaintenanceClock(ctx)
	if err != nil {
		return p.result, maintenanceError(err)
	}
	out, err := p.store.maintenanceFinish(ctx, p.conn, p.actor, p.command, p.id, p.permit, p.frozen, p.state, stamp.Time, trusted, p.result)
	return out, maintenanceError(err)
}

var _ ports.PreparedLibraryMaintenance = (*maintenanceExecution)(nil)
