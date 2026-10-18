package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
)

var (
	ErrStaleLease      = errors.New("lease expired or held by another owner: write rejected")
	ErrInvalidDuration = errors.New("job duration must be a positive int32 millisecond interval")
)

type JobHandler func(ctx context.Context, job *db.Job) (result any, err error)

// Queue coordinates persistent job enqueuing, atomic claim, heartbeat lease renewal, and completion.
type Queue struct {
	pool     *pgxpool.Pool
	idgen    ports.IDGen
	clock    ports.Clock
	queries  *db.Queries
	handlers map[string]JobHandler
	mu       sync.RWMutex
}

func NewQueue(pool *pgxpool.Pool, idgen ports.IDGen, clock ports.Clock) *Queue {
	return &Queue{
		pool:     pool,
		idgen:    idgen,
		clock:    clock,
		queries:  postgres.Queries(pool),
		handlers: make(map[string]JobHandler),
	}
}

// RegisterHandler registers an explicit handler function for a job kind.
func (q *Queue) RegisterHandler(kind string, handler JobHandler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[kind] = handler
}

// Enqueue inserts a new job into the queue, honoring deduplication key if present.
func (q *Queue) Enqueue(ctx context.Context, kind, dedupeKey string, priority int32, payload any, notBefore time.Time, maxAttempts int32) (*db.Job, error) {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if notBefore.IsZero() {
		notBefore = q.clock.Now()
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal job payload: %w", err)
	}

	jobID := q.idgen.NewUUID()

	row, err := q.queries.EnqueueJob(ctx, db.EnqueueJobParams{
		ID:          jobID,
		Kind:        kind,
		DedupeKey:   pgtype.Text{String: dedupeKey, Valid: dedupeKey != ""},
		Priority:    priority,
		Payload:     payloadBytes,
		MaxAttempts: maxAttempts,
		NotBefore:   pgtype.Timestamptz{Time: notBefore, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to enqueue job: %w", err)
	}
	return &row, nil
}

// Claim atomically claims the highest priority ready job using SKIP LOCKED.
func (q *Queue) Claim(ctx context.Context, kinds []string, owner string, leaseDuration time.Duration) (*db.Job, error) {
	leaseMs, err := durationMilliseconds(leaseDuration, 30*time.Second)
	if err != nil {
		return nil, err
	}

	job, err := q.queries.ClaimJob(ctx, db.ClaimJobParams{
		Column1: kinds,
		Column2: owner,
		Column3: leaseMs,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // No jobs ready for claim
		}
		return nil, fmt.Errorf("failed to claim job: %w", err)
	}
	return &job, nil
}

// Finish marks a running job as succeeded, strictly enforcing valid lease and current database clock.
func (q *Queue) Finish(ctx context.Context, jobID uuid.UUID, owner string, leaseToken int64, result any) error {
	tx, err := q.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin finish transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	qtx := q.queries.WithTx(tx)

	// 1. Lock job row for write
	_, err = qtx.LockJobForWrite(ctx, jobID)
	if err != nil {
		return fmt.Errorf("failed to lock job row: %w", err)
	}

	var resBytes []byte
	if result != nil {
		resBytes, err = json.Marshal(result)
		if err != nil {
			return fmt.Errorf("failed to marshal job result: %w", err)
		}
	}

	// 2. Finish checking current DB time clock_timestamp() > lease_until
	row, err := qtx.FinishJob(ctx, db.FinishJobParams{
		Column1: jobID,
		Column2: owner,
		Column3: leaseToken,
		Column4: resBytes,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Updated 0 rows -> lease expired or stolen!
			return ErrStaleLease
		}
		return fmt.Errorf("failed to finish job: %w", err)
	}
	_ = row

	return tx.Commit(ctx)
}

// Fail marks a running job as failed (or dead if attempts exhausted), scheduling retry if eligible.
func (q *Queue) Fail(ctx context.Context, jobID uuid.UUID, owner string, leaseToken int64, errMsg string, retryAfter time.Duration) error {
	retryMs, err := durationMilliseconds(retryAfter, 5*time.Second)
	if err != nil {
		return err
	}

	_, err = q.queries.FailJob(ctx, db.FailJobParams{
		Column1: jobID,
		Column2: owner,
		Column3: leaseToken,
		Column4: errMsg,
		Column5: retryMs,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleLease
		}
		return fmt.Errorf("failed to fail job: %w", err)
	}
	return nil
}

// RenewLease extends the lease duration of an actively running job.
func (q *Queue) RenewLease(ctx context.Context, jobID uuid.UUID, owner string, leaseToken int64, additional time.Duration) error {
	addMs, err := durationMilliseconds(additional, 0)
	if err != nil {
		return err
	}
	_, err = q.queries.RenewJobLease(ctx, db.RenewJobLeaseParams{
		Column1: jobID,
		Column2: owner,
		Column3: leaseToken,
		Column4: addMs,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleLease
		}
		return fmt.Errorf("failed to renew lease: %w", err)
	}
	return nil
}

// durationMilliseconds validates before narrowing; only claim/retry allow defaults.
func durationMilliseconds(value, defaultValue time.Duration) (int32, error) {
	if value <= 0 && defaultValue > 0 {
		value = defaultValue
	}
	millis := value.Milliseconds()
	if millis <= 0 || millis > math.MaxInt32 {
		return 0, ErrInvalidDuration
	}
	return int32(millis), nil
}
