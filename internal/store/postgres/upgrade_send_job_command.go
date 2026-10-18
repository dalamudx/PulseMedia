package postgres

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"time"
)

type movieUpgradeSendJobStore struct{ store *UpgradePreparationStore }

func NewMovieUpgradeSendCommandRepository(s *UpgradePreparationStore) (ports.MovieUpgradeSendCommandRepository, error) {
	if s == nil {
		return nil, preparationConflict()
	}
	return &movieUpgradeSendJobStore{store: s}, nil
}
func sendJobCommandReceipt(raw json.RawMessage) (*ports.MovieUpgradeSendJobReceipt, error) {
	var row struct {
		OperationID uuid.UUID `json:"operation_id"`
		TaskID      uuid.UUID `json:"task_id"`
		CommandID   uuid.UUID `json:"command_id"`
		JobID       uuid.UUID `json:"job_id"`
		CommandedAt time.Time `json:"commanded_at"`
	}
	if json.Unmarshal(raw, &row) != nil || row.CommandID == uuid.Nil || row.JobID == uuid.Nil {
		return nil, preparationConflict()
	}
	return &ports.MovieUpgradeSendJobReceipt{OperationID: row.OperationID, TaskID: row.TaskID, CommandID: row.CommandID, JobID: row.JobID, QueuedAt: row.CommandedAt, Status: "queued", ExecutionAllowed: false}, nil
}
func (r *movieUpgradeSendJobStore) QueueMovieUpgradeSend(ctx context.Context, a *ports.Actor, c ports.QueueMovieUpgradeSend, key string) (out *ports.MovieUpgradeSendJobReceipt, err error) {
	if e := episodeReviewActor(a); e != nil {
		return nil, e
	}
	if !c.Confirmed || c.OperationID == uuid.Nil || c.TaskID == uuid.Nil || !upgradeKey(key) || len(key) > 100 {
		return nil, preparationConflict()
	}
	defer func() { err = upgradeError(ctx, err) }()
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := r.store.pool.Begin(work)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(work, tx)
	authorize := func() (*ports.Actor, uuid.UUID, error) {
		current, subscriptionID, err := r.store.authorizePreparation(work, tx, a, c.OperationID)
		if err != nil {
			return nil, uuid.Nil, err
		}
		if c.SubscriptionID != uuid.Nil && c.SubscriptionID != subscriptionID {
			return nil, uuid.Nil, preparationConflict()
		}
		return current, subscriptionID, nil
	}
	a, sub, e := authorize()
	if e != nil {
		return nil, e
	}
	q := db.New(tx)
	if e = q.LockDownloadAcquisition(work, c.OperationID); e != nil {
		return nil, e
	}
	hash := coverageDigest([]any{"upgrade.queue-send.v1", c})
	var old ports.MovieUpgradeSendJobReceipt
	found, e := upgradeReceipt(work, q, a, "upgrade.queue-send:"+key, hash, &old)
	if e != nil {
		return nil, e
	}
	if found {
		var raw json.RawMessage
		if e = tx.QueryRow(work, `SELECT to_jsonb(b) FROM movie_upgrade_send_job b WHERE operation_id=$1 AND command_key=$2 AND task_id=$3 AND actor_id=$4::uuid`, c.OperationID, key, c.TaskID, a.ID).Scan(&raw); e != nil {
			return nil, e
		}
		expected, e := sendJobCommandReceipt(raw)
		if e != nil {
			return nil, e
		}
		want, _ := json.Marshal(expected)
		got, _ := json.Marshal(old)
		if !preparationSameJSON(want, got) {
			return nil, preparationConflict()
		}
		own, e := readUpgradeObservationLedger(work, tx, c.OperationID, a, sub, key, hash, "upgrade.queue-send", expected, true)
		if e != nil {
			return nil, e
		}
		if _, _, e = authorize(); e != nil {
			return nil, e
		}
		if e = own.verify(work, tx); e != nil {
			return nil, e
		}
		if e = tx.Commit(work); e != nil {
			return nil, e
		}
		return expected, nil
	}
	b, e := r.store.loadMovieUpgradeBasis(work, tx, a, c.OperationID, true)
	if e != nil {
		return nil, e
	}
	if e = bindPreparedUpgradeBasis(work, tx, b); e != nil {
		return nil, e
	}
	var task uuid.UUID
	var available bool
	if e = tx.QueryRow(work, `SELECT p.task_id,NOT EXISTS(SELECT 1 FROM movie_upgrade_send_job WHERE operation_id=p.operation_id) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_authorization WHERE operation_id=p.operation_id) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_observation WHERE operation_id=p.operation_id) FROM movie_upgrade_preparation p WHERE p.operation_id=$1`, c.OperationID).Scan(&task, &available); e != nil {
		return nil, e
	}
	if task != c.TaskID || !available {
		return nil, preparationConflict()
	}
	jobID, commandID := r.store.ids.NewUUID(), r.store.ids.NewUUID()
	var job, command json.RawMessage
	e = tx.QueryRow(work, `SELECT to_jsonb(jsonb_populate_record(NULL::job,jsonb_build_object('id',$1::uuid,'kind',$2::text,'dedupe_key',$3::text,'payload',jsonb_build_object('command_id',$4::uuid,'operation_id',$5::uuid,'task_id',$6::uuid,'actor_id',$7::uuid),'state','queued','priority',0,'attempts',0,'max_attempts',1,'lease_token',0,'created_at',n,'updated_at',n,'not_before',n))) FROM (SELECT clock_timestamp() n) x`, jobID, ports.MovieUpgradeSendJobKind, "movie-upgrade-send:"+c.OperationID.String(), commandID, c.OperationID, c.TaskID, a.ID).Scan(&job)
	if e != nil {
		return nil, e
	}
	e = tx.QueryRow(work, `SELECT jsonb_build_object('job_id',$1::uuid,'command_id',$2::uuid,'operation_id',p.operation_id,'task_id',p.task_id,'actor_id',p.actor_id,'command_key',$4::text,'send_key','job:'||$2::uuid::text,'prepared_sha256',sha256(convert_to(to_jsonb(p)::text,'UTF8')),'original_sha256',pulse_upgrade_send_job_original(p.operation_id),'protected_sha256',pulse_upgrade_send_job_protected(p.operation_id),'commanded_at',($5::jsonb->>'created_at')::timestamptz,'queued_job',$5::jsonb) FROM movie_upgrade_preparation p WHERE operation_id=$3`, jobID, commandID, c.OperationID, key, job).Scan(&command)
	if e != nil {
		return nil, e
	}
	for _, v := range []struct {
		table string
		value json.RawMessage
	}{{"movie_upgrade_send_job", command}, {"job", job}} {
		var actual json.RawMessage
		if e = tx.QueryRow(work, "INSERT INTO "+v.table+" SELECT * FROM jsonb_populate_record(NULL::"+v.table+",$1::jsonb) RETURNING to_jsonb("+v.table+")", v.value).Scan(&actual); e != nil {
			return nil, e
		}
		if !preparationSameJSON(actual, v.value) {
			return nil, preparationConflict()
		}
	}
	out, e = sendJobCommandReceipt(command)
	if e != nil {
		return nil, e
	}
	own, e := readUpgradeObservationLedger(work, tx, c.OperationID, a, sub, key, hash, "upgrade.queue-send", out, false)
	if e != nil {
		return nil, e
	}
	if e = bindSendJobBasis(work, tx, b); e != nil {
		return nil, e
	}
	check := func() error {
		if _, _, e := authorize(); e != nil {
			return e
		}
		if e := b.check(work, tx); e != nil {
			return e
		}
		var same bool
		if e := tx.QueryRow(work, `SELECT to_jsonb(b)=$2::jsonb AND to_jsonb(j)=$3::jsonb AND b.protected_sha256=pulse_upgrade_send_job_protected(b.operation_id) FROM movie_upgrade_send_job b JOIN job j ON j.id=b.job_id WHERE b.operation_id=$1`, c.OperationID, command, job).Scan(&same); e != nil {
			return e
		}
		if !same {
			return preparationConflict()
		}
		return own.verify(work, tx)
	}
	if e = check(); e != nil {
		return nil, e
	}
	s := &UpgradeReservationStore{pool: r.store.pool, ids: r.store.ids}
	if e = s.upgradeSuccess(work, tx, a, sub, "upgrade.queue-send:"+key, hash, "upgrade.queue-send", c.OperationID, out, own, check); e != nil {
		return nil, e
	}
	ledger, e := readUpgradeObservationLedger(work, tx, c.OperationID, a, sub, key, hash, "upgrade.queue-send", out, true)
	if e != nil {
		return nil, e
	}
	if e = ledger.verify(work, tx); e != nil {
		return nil, e
	}
	if e = check(); e != nil {
		return nil, e
	}
	if e = tx.Commit(work); e != nil {
		return nil, e
	}
	return out, nil
}

var _ ports.MovieUpgradeSendCommandRepository = (*movieUpgradeSendJobStore)(nil)
