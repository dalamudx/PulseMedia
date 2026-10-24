package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedingScheduleStore queues ordinary tasks with complete original ingestion
// evidence. Queueing has no native capability and cannot issue a removal permit.
type SeedingScheduleStore struct {
	pool *pgxpool.Pool
	ids  ports.IDGen
}

func NewSeedingScheduleStore(pool *pgxpool.Pool, ids ports.IDGen) *SeedingScheduleStore {
	return &SeedingScheduleStore{pool: pool, ids: ids}
}

type seedingJobPayload struct {
	TaskID        uuid.UUID `json:"task_id"`
	ClientID      uuid.UUID `json:"client_id"`
	ConfigVersion int64     `json:"config_version"`
}

func seedingRuntimeGuard(ctx context.Context, q *db.Queries) error {
	isolated, err := q.GuardDoubanSyncIsolation(ctx)
	if err != nil || isolated {
		return ports.ErrUnavailable
	}
	return nil
}
func seedingRuntimeEvent(ctx context.Context, q *db.Queries, ids ports.IDGen, task, job uuid.UUID, version int64, action string) error {
	payload, _ := json.Marshal(struct {
		JobID         uuid.UUID `json:"job_id"`
		ConfigVersion int64     `json:"configuration_version"`
	}{job, version})
	if _, err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: ids.NewUUID(), ActorType: string(ports.ActorTypeSystem), ActorID: "seeding-scheduler", Action: action, TargetType: "download_task", TargetID: task.String(), Payload: payload}); err != nil {
		return err
	}
	aggregate := db.LockOutboxAggregateParams{AggregateType: "seeding.removal", AggregateID: task.String()}
	if err := q.LockOutboxAggregate(ctx, aggregate); err != nil {
		return err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams(aggregate))
	if err != nil {
		return err
	}
	_, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: ids.NewUUID(), AggregateType: aggregate.AggregateType, AggregateID: aggregate.AggregateID, EventType: action, Payload: payload, Seq: int64(seq)})
	return err
}

// EnqueueDue serializes bounded scheduling batches, not network execution. An
// active dispatch retains its frozen owner/configuration even after edits.
func (s *SeedingScheduleStore) EnqueueDue(ctx context.Context) (err error) {
	defer func() { err = seedingConfigError(err) }()
	if err = ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.pool == nil || s.ids == nil {
		return ports.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = seedingRuntimeGuard(ctx, q); err != nil {
		return err
	}
	if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: "seeding.scheduler", AggregateID: "global"}); err != nil {
		return err
	}
	candidates, err := q.ListDueSeedingTasks(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		actor := &ports.Actor{Type: ports.ActorTypeUser, ID: candidate.OwnerID.String(), Role: ports.RoleAdmin}
		if err = settingUser(ctx, q, actor, true); errors.Is(err, ports.ErrForbidden) {
			continue
		} else if err != nil {
			return err
		}
		if err = q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: seedingConfigAction, AggregateID: candidate.ClientID.String()}); err != nil {
			return err
		}
		endpoint, e := q.LockDownloadClientForEdit(ctx, candidate.ClientID)
		if e != nil {
			return e
		}
		config, e := q.LockSeedingConfig(ctx, candidate.ClientID)
		if e != nil {
			return e
		}
		view, e := seedingConfigView(config)
		if e != nil {
			return e
		}
		if !view.Enabled || config.Version != candidate.ConfigVersion || config.OwnerID != candidate.OwnerID || config.ClientStamp != candidate.ClientStamp || !endpoint.UpdatedAt.Time.Equal(config.ClientUpdatedAt.Time) {
			continue
		}
		if err = q.LockDownloadAcquisition(ctx, candidate.OperationID); err != nil {
			return err
		}
		task, e := q.GetSubmissionTask(ctx, candidate.TaskID)
		if e != nil {
			return e
		}
		_, e = q.GetSeedingRemovalPermit(ctx, task.ID)
		if e == nil {
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		proof, e := q.ReadOrdinarySeedingProtection(ctx, db.ReadOrdinarySeedingProtectionParams{TaskID: task.ID, ClientStamp: config.ClientStamp})
		if e != nil {
			return e
		}
		if !proof.Owned || !proof.Imported || proof.PendingWork {
			continue
		}
		schedule, e := q.GetSeedingRemovalSchedule(ctx, task.ID)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		due := config.UpdatedAt.Time
		if task.UpdatedAt.Time.After(due) {
			due = task.UpdatedAt.Time
		}
		due = due.Add(time.Duration(config.IntervalSeconds) * time.Second)
		if schedule.TaskID != uuid.Nil {
			if schedule.State != "idle" || schedule.ActiveJobID.Valid {
				continue
			}
			if schedule.ConfigVersion == config.Version {
				due = schedule.NextDueAt.Time
			}
		}
		now, e := q.GetSearchDatabaseTime(ctx)
		if e != nil {
			return e
		}
		if !now.Valid {
			return ports.ErrUnavailable
		}
		if due.After(now.Time) {
			continue
		}
		payload, _ := json.Marshal(seedingJobPayload{task.ID, config.ClientID, config.Version})
		job, e := q.CreateSeedingRemovalJob(ctx, db.CreateSeedingRemovalJobParams{ID: s.ids.NewUUID(), DedupeKey: pgtype.Text{String: task.ID.String(), Valid: true}, Payload: payload})
		if e != nil {
			return e
		}
		changed, e := q.QueueSeedingRemovalSchedule(ctx, db.QueueSeedingRemovalScheduleParams{TaskID: task.ID, ConfigVersion: config.Version, ActiveJobID: pgtype.UUID{Bytes: job.ID, Valid: true}})
		if e != nil {
			return e
		}
		if changed != 1 {
			return ports.ErrUnavailable
		}
		if err = q.CreateSeedingRemovalDispatch(ctx, db.CreateSeedingRemovalDispatchParams{JobID: job.ID, TaskID: task.ID, ClientID: config.ClientID, ClientStamp: config.ClientStamp, ConfigVersion: config.Version, OwnerID: config.OwnerID}); err != nil {
			return err
		}
		if err = seedingRuntimeEvent(ctx, q, s.ids, task.ID, job.ID, config.Version, "seeding.removal.queued"); err != nil {
			return err
		}
	}
	if err = seedingRuntimeGuard(ctx, q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
