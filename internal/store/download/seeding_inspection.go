package download

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"sync"
)

var _ ports.SeedingInspectionRepository = (*Repository)(nil)

func (m *Manager) seedingInspectionSnapshot(ctx context.Context, a *ports.Actor, op uuid.UUID) (controlSnapshot, error) {
	var out controlSnapshot
	if err := m.requireTVEpisodeUpgradeIsolation(ctx, op); err != nil {
		return out, err
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer rollbackControl(tx)
	q := db.New(tx)
	if err = controlUser(ctx, q, a, false); err != nil {
		return out, err
	}
	requirements, err := q.ListRequirementsForOperation(ctx, op)
	if err != nil {
		return out, err
	}
	if err = controlRequirements(a, requirements, false, false); err != nil {
		return out, err
	}
	task, err := q.GetDownloadTaskByOpID(ctx, op)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ports.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if err = m.requireNoFrozenUpgrade(ctx, op); err != nil {
		return out, err
	}
	if err = m.requireGenericObservation(ctx, op); err != nil {
		return out, err
	}
	if task.State != "tracking" && task.State != "completed" {
		return out, ports.ErrDownloadControlConflict
	}
	hash := strings.ToLower(task.InfoHash)
	if _, err = hex.DecodeString(hash); err != nil || (len(hash) != 40 && len(hash) != 64) || task.SavePath == "" {
		return out, ports.ErrDownloadControlConflict
	}
	bound, err := q.ReadBoundSeedingInspectionClient(ctx, task.ClientID)
	if err != nil {
		return out, err
	}
	binding, err := q.GetDownloadTaskClientBinding(ctx, task.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ports.ErrDownloadControlConflict
	}
	if err != nil {
		return out, err
	}
	if binding.ClientID != task.ClientID || binding.ClientStamp != bound.ConfigStamp {
		return out, ports.ErrDownloadControlConflict
	}
	raw, err := json.Marshal(requirements)
	if err != nil {
		return out, err
	}
	out = controlSnapshot{task: task, client: bound.DownloadClient, stamp: bound.ConfigStamp, requirements: raw}
	if err = tx.Commit(ctx); err != nil {
		return controlSnapshot{}, err
	}
	return out, nil
}

func (r *Repository) PrepareSeedingInspection(ctx context.Context, a *ports.Actor, op uuid.UUID) (*ports.PreparedSeedingInspection, error) {
	if err := controlActor(a); err != nil {
		return nil, err
	}
	actor := *a
	m := r.execution
	initial, err := m.seedingInspectionSnapshot(ctx, &actor, op)
	if err != nil {
		return nil, err
	}
	release, err := m.acquireClientBudget(ctx, initial.task.ClientID)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			release()
		}
	}()
	validate := func(c context.Context) error {
		current, e := m.seedingInspectionSnapshot(c, &actor, op)
		if e != nil {
			return e
		}
		if !initial.equal(current) {
			return ports.ErrDownloadControlConflict
		}
		return nil
	}
	if err = validate(ctx); err != nil {
		return nil, err
	}
	client, err := m.clientForRateSnapshot(ctx, initial.client)
	if err != nil {
		return nil, err
	}
	capability, ok := client.(interface {
		SeedingObserver() ports.SeedingReadClient
	})
	if !ok {
		return nil, ports.ErrDownloadControlUnsupported
	}
	reader := capability.SeedingObserver()
	if reader == nil {
		return nil, ports.ErrDownloadControlUnsupported
	}
	var once sync.Once
	result := &ports.PreparedSeedingInspection{Reader: reader, InfoHash: strings.ToLower(initial.task.InfoHash), OperationTag: "pulsemedia:op:" + op.String(), SavePath: initial.task.SavePath, Validate: validate, Release: func() { once.Do(release) }}
	retained = true
	return result, nil
}
