package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PersonFollowSyncStore struct {
	pool           *pgxpool.Pool
	ids            ports.IDGen
	scheduleMu     sync.Mutex
	scheduleCursor db.ListDuePersonFollowConfigsParams
	scheduleLimits *ScheduleLimits
}

func NewPersonFollowSyncStore(pool *pgxpool.Pool, ids ports.IDGen, limits ...*ScheduleLimits) *PersonFollowSyncStore {
	return &PersonFollowSyncStore{pool: pool, ids: ids, scheduleLimits: optionalScheduleLimits(limits)}
}

type personFollowPayload struct {
	Version int       `json:"version"`
	RunID   uuid.UUID `json:"run_id"`
	Start   int       `json:"start"`
}

func personFollowPayloadJSON(run uuid.UUID, start int) []byte {
	raw, _ := json.Marshal(personFollowPayload{Version: 1, RunID: run, Start: start})
	return raw
}

// One transaction per configuration keeps failures independent and freezes the
// same verified configuration history used by later pages and subscription creation.
func (s *PersonFollowSyncStore) EnqueueDue(ctx context.Context) (err error) {
	defer func(parent context.Context) { err = personFollowError(parent, err) }(ctx)
	if s == nil || s.pool == nil || s.ids == nil {
		return ports.ErrUnavailable
	}
	// Overlapping ticks share one scan cursor; an active scan already owns the work.
	if !s.scheduleMu.TryLock() {
		return nil
	}
	defer s.scheduleMu.Unlock()
	ctx, cancel := s.scheduleLimits.bound(ctx)
	defer cancel()
	q := db.New(s.pool)
	candidates, err := q.ListDuePersonFollowConfigs(ctx, s.scheduleCursor)
	if err != nil {
		return err
	}
	if len(candidates) == 0 && s.scheduleCursor.AfterID != uuid.Nil {
		s.scheduleCursor = db.ListDuePersonFollowConfigsParams{}
		candidates, err = q.ListDuePersonFollowConfigs(ctx, s.scheduleCursor)
		if err != nil {
			return err
		}
	}
	var firstErr error
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if candidateErr := s.scheduleLimits.run(ctx, candidate.ID, candidate.Version, func() error { return s.enqueue(ctx, candidate.ID, candidate.OwnerID) }); candidateErr != nil && firstErr == nil {
			firstErr = candidateErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.scheduleCursor = db.ListDuePersonFollowConfigsParams{AfterOwnerID: candidate.OwnerID, AfterID: candidate.ID}
	}
	if len(candidates) < 64 {
		s.scheduleCursor = db.ListDuePersonFollowConfigsParams{}
	}
	return firstErr
}
func (s *PersonFollowSyncStore) enqueue(ctx context.Context, id, owner uuid.UUID) error {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(bounded)
	if err != nil {
		return err
	}
	defer managementRollback(bounded, tx)
	q := db.New(tx)
	user, err := q.LockPersonFollowOwner(bounded, owner)
	if err != nil {
		return err
	}
	if !user.IsActive || (user.Role != ports.RoleAdmin && user.Role != ports.RoleMember) {
		return nil
	}
	isolated, err := q.GuardDoubanSyncIsolation(bounded)
	if err != nil {
		return err
	}
	if isolated {
		return ports.ErrUnavailable
	}
	row, err := q.LockPersonFollowConfig(bounded, id)
	if err != nil {
		return err
	}
	if !row.Enabled || row.OwnerID != owner {
		return nil
	}
	due, err := q.PersonFollowConfigDue(bounded, db.PersonFollowConfigDueParams{ConfigID: id, ConfigVersion: row.Version, IntervalMinutes: row.IntervalMinutes})
	if err != nil || !due {
		return err
	}
	current, err := personFollowSnapshot(bounded, q, row)
	if err != nil {
		return err
	}
	history, err := q.ReadPersonFollowVersion(bounded, db.ReadPersonFollowVersionParams{ConfigID: id, Version: row.Version})
	if err != nil {
		return err
	}
	var frozen ports.PersonFollowConfig
	if !creationStrictJSON(history.Snapshot, &frozen) || !personFollowSameConfig(current, frozen) || history.OwnerID != owner || !strings.HasPrefix(history.CommandKey, personFollowNamespace) {
		return ports.ErrUnavailable
	}
	actor := ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: user.Role}
	key := strings.TrimPrefix(history.CommandKey, personFollowNamespace)
	receipt, _, err := personFollowReceipt(bounded, q, &actor, key)
	if err != nil {
		return err
	}
	if !personFollowSameConfig(receipt.Config, frozen) {
		return ports.ErrPersonFollowConflict
	}
	if err = personFollowTargets(bounded, q, current.Settings); err != nil {
		if errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrRejected) || errors.Is(err, ports.ErrPersonFollowConflict) {
			return nil
		}
		return err
	}
	run, job := s.ids.NewUUID(), s.ids.NewUUID()
	if err = q.InsertPersonFollowRun(bounded, db.InsertPersonFollowRunParams{ID: run, ConfigID: id, ConfigVersion: current.Version, OwnerID: owner, ConfigSnapshot: history.Snapshot}); err != nil {
		return err
	}
	if err = q.InsertPersonFollowSyncJob(bounded, db.InsertPersonFollowSyncJobParams{ID: job, Payload: personFollowPayloadJSON(run, 0)}); err != nil {
		return err
	}
	if err = q.InsertPersonFollowSyncPage(bounded, db.InsertPersonFollowSyncPageParams{JobID: job, RunID: run, PageStart: 0}); err != nil {
		return err
	}
	if err = personFollowLockOwner(bounded, q, &actor); err != nil {
		return err
	}
	if err = personFollowTargets(bounded, q, current.Settings); err != nil {
		return err
	}
	return tx.Commit(bounded)
}
