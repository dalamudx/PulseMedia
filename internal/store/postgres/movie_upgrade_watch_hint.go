package postgres

import (
	"context"
	"errors"
	"path"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type MovieUpgradeWatchStore struct {
	runtime *MovieUpgradeTrackingRuntimeStore
	clock   ports.Clock
}

func NewMovieUpgradeWatchStore(runtime *MovieUpgradeTrackingRuntimeStore, clock ports.Clock) (*MovieUpgradeWatchStore, error) {
	if runtime == nil || runtime.store == nil || runtime.store.pool == nil || clock == nil {
		return nil, ports.ErrUnavailable
	}
	return &MovieUpgradeWatchStore{runtime: runtime, clock: clock}, nil
}

var _ ports.DownloadWatchRepository = (*MovieUpgradeWatchStore)(nil)
var _ ports.MovieUpgradeTrackingHintScheduler = (*MovieUpgradeWatchStore)(nil)

func movieUpgradeWatchError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, ports.ErrDownloadWatchStale) || errors.Is(err, ports.ErrRejected) || errors.Is(err, ErrRestoreIsolated) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrUnauthorized) || errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrUpgradeReservationConflict) || errors.Is(err, ports.ErrMovieUpgradeTrackingReview) {
		return ports.ErrDownloadWatchStale
	}
	return upgradeObservationError(ctx, err)
}

func (s *MovieUpgradeWatchStore) remaining(r ports.MovieUpgradeTrackingHintRequest) (time.Duration, error) {
	if s == nil || s.runtime == nil || s.clock == nil {
		return 0, ports.ErrUnavailable
	}
	if r.EventID == uuid.Nil || r.InstanceID == uuid.Nil || r.Sequence <= 0 || r.SourceIdentity.Inode <= 0 || r.SourceIdentity.Device < 0 || r.ReceivedAt.IsZero() || r.ManifestAt.IsZero() || r.Hint.Metadata != (ports.RootChangeMetadata{}) || !watchHint(r.Hint) || (r.Hint.Kind != ports.RootChangeCloseWrite && r.Hint.Kind != ports.RootChangeMovedTo) {
		return 0, ports.ErrRejected
	}
	if r.Binding.MediaType != "movie" {
		return 0, ports.ErrDownloadWatchStale
	}
	if r.Binding.LocalRoot == r.Binding.RootPath && (r.SourceIdentity.Device != r.Binding.RootIdentity.Device || r.SourceIdentity.Inode != r.Binding.RootIdentity.Inode) {
		return 0, ports.ErrDownloadWatchStale
	}
	age := s.clock.Since(r.ReceivedAt)
	if age < 0 {
		return 0, ports.ErrRejected
	}
	if age >= movieUpgradeWatchTTL {
		return 0, ports.ErrDownloadWatchStale
	}
	return movieUpgradeWatchTTL - age, nil
}

type movieUpgradeWatchCandidate struct {
	watchTaskMaterial
	ActorID            uuid.UUID `json:"actor_id"`
	RootID             uuid.UUID `json:"root_id"`
	PriorObservationID uuid.UUID `json:"prior_observation_id"`
	Due                time.Time `json:"due"`
	PreparedPaths      []string  `json:"prepared_paths"`
}

func movieUpgradeWatchPreparedPath(c movieUpgradeWatchCandidate, b ports.DownloadWatchBinding, h ports.RootChangeHint) bool {
	if len(c.PreparedPaths) < 1 || len(c.PreparedPaths) > 1000 {
		return false
	}
	matched := false
	for _, relative := range c.PreparedPaths {
		if !watchHint(ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: relative}) || relative == "." {
			return false
		}
		task := c.watchTaskMaterial
		task.Eligible = true
		task.ContentPath = path.Join(task.SavePath, relative)
		if watchTaskBinding(task, b, h) && path.Join(b.LocalRoot, h.RelativePath) == automaticTVWatchLocalPath(task.Root.Mapping, task.ContentPath) {
			matched = true
		}
	}
	return matched
}

func movieUpgradeWatchDigest(c movieUpgradeWatchCandidate) string {
	return watchDigest(struct {
		Graph string
		Root  watchRootMaterial
	}{c.TaskStamp, c.Root})
}

func (s *MovieUpgradeWatchStore) facts(ctx context.Context, tx pgx.Tx, r ports.MovieUpgradeTrackingHintRequest, own uuid.UUID) (string, time.Time, error) {
	q := db.New(tx)
	b := r.Binding
	b.ClientVersion = b.ClientVersion.UTC()
	if err := watchCurrentBinding(ctx, q, b); err != nil {
		return "", time.Time{}, err
	}
	raw, err := q.GetDownloadWatchRootMaterial(ctx, db.GetDownloadWatchRootMaterialParams{ClientID: b.ClientID, RootID: b.RootID})
	if err != nil {
		return "", time.Time{}, err
	}
	var root watchRootMaterial
	if err = statsDecode(raw, &root); err != nil {
		return "", time.Time{}, ports.ErrUnavailable
	}
	raw, err = q.GetMovieUpgradeWatchTask(ctx, db.GetMovieUpgradeWatchTaskParams{OperationID: r.OperationID, TaskID: r.TaskID, PriorObservationID: r.PriorObservationID, OwnJob: own, ManifestAt: pgtype.Timestamptz{Time: r.ManifestAt, Valid: true}})
	if err != nil {
		return "", time.Time{}, err
	}
	var c movieUpgradeWatchCandidate
	if err = statsDecode(raw, &c); err != nil {
		return "", time.Time{}, ports.ErrUnavailable
	}
	c.Root = root
	if c.RootID != b.RootID || c.ClientID != b.ClientID || !movieUpgradeWatchPreparedPath(c, b, r.Hint) {
		return "", time.Time{}, ports.ErrDownloadWatchStale
	}
	return movieUpgradeWatchDigest(c), c.Due, nil
}

// Original actor construction and original preparation/return-ledger locks only.
func (s *MovieUpgradeWatchStore) load(ctx context.Context, tx pgx.Tx, r ports.MovieUpgradeTrackingHintRequest) (upgradeTrackingHint, error) {
	h := upgradeTrackingHint{operation: r.OperationID, task: r.TaskID, prior: &r.PriorObservationID}
	if r.OperationID == uuid.Nil || r.TaskID == uuid.Nil || r.PriorObservationID == uuid.Nil {
		return h, ports.ErrRejected
	}
	if err := tx.QueryRow(ctx, `SELECT actor_id FROM movie_upgrade_preparation WHERE operation_id=$1 AND task_id=$2`, r.OperationID, r.TaskID).Scan(&h.actor); err != nil {
		return h, err
	}
	a, err := s.runtime.actor(ctx, tx, h)
	if err != nil {
		return h, err
	}
	session := &movieUpgradeTrackingSession{base: &movieUpgradeSendSession{store: s.runtime.store, operation: r.OperationID}}
	_, _, verify, err := session.load(ctx, tx, a)
	if err != nil {
		return h, err
	}
	if err = verify(); err != nil {
		return h, err
	}
	return h, nil
}

func (s *MovieUpgradeWatchStore) ResolveMovieUpgradeTrackingHint(ctx context.Context, r ports.MovieUpgradeTrackingHintRequest) (out ports.MovieUpgradeTrackingHintCapture, err error) {
	defer func() { err = movieUpgradeWatchError(ctx, err) }()
	remaining, err := s.remaining(r)
	if err != nil {
		return nil, err
	}
	tx, _, err := (&DownloadWatchStore{pool: s.runtime.store.pool}).read(ctx)
	if err != nil {
		return nil, err
	}
	defer managementRollback(ctx, tx)
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if r.ManifestAt.After(now) {
		return nil, ports.ErrDownloadWatchStale
	}
	h, err := s.load(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	digest, due, err := s.facts(ctx, tx, r, uuid.Nil)
	if err != nil {
		return nil, err
	}
	c := &movieUpgradeWatchCapture{request: r, store: s, hint: h, digest: digest, deadline: now.Add(remaining), due: due}
	if err = s.runtime.validateMovieUpgradeWatchCapture(ctx, tx, c, uuid.Nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *MovieUpgradeWatchStore) private(capture ports.MovieUpgradeTrackingHintCapture) (*movieUpgradeWatchCapture, error) {
	c, ok := capture.(*movieUpgradeWatchCapture)
	if !ok || c == nil || c.store != s || s == nil {
		return nil, ports.ErrDownloadWatchStale
	}
	if _, err := s.remaining(c.request); err != nil {
		return nil, err
	}
	return c, nil
}

// No DML, no new capture and no caller-controlled root I/O.
func (s *MovieUpgradeWatchStore) ValidateMovieUpgradeTrackingHint(ctx context.Context, capture ports.MovieUpgradeTrackingHintCapture) (err error) {
	defer func() { err = movieUpgradeWatchError(ctx, err) }()
	c, err := s.private(capture)
	if err != nil {
		return err
	}
	_, err = s.validateRead(ctx, c)
	return err
}

func (s *MovieUpgradeWatchStore) validateRead(ctx context.Context, c *movieUpgradeWatchCapture) (time.Time, error) {
	tx, _, err := (&DownloadWatchStore{pool: s.runtime.store.pool}).read(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer managementRollback(ctx, tx)
	if _, err = s.load(ctx, tx, c.request); err != nil {
		return time.Time{}, err
	}
	if err = s.runtime.validateMovieUpgradeWatchCapture(ctx, tx, c, uuid.Nil); err != nil {
		return time.Time{}, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return now, err
	}
	if !now.Before(c.deadline) {
		return now, ports.ErrDownloadWatchStale
	}
	return now, tx.Commit(ctx)
}

func (s *MovieUpgradeWatchStore) ScheduleMovieUpgradeTrackingHint(ctx context.Context, capture ports.MovieUpgradeTrackingHintCapture) (out *ports.MovieUpgradeTrackingHintScheduleResult, err error) {
	defer func() {
		err = movieUpgradeWatchError(ctx, err)
		if err != nil {
			out = nil
		}
	}()
	c, err := s.private(capture)
	if err != nil {
		return nil, err
	}
	now, err := s.validateRead(ctx, c)
	if err != nil {
		return nil, err
	}
	remaining, err := s.remaining(c.request)
	if err != nil {
		return nil, err
	}
	wait := c.due.Sub(now)
	if wait > 0 {
		if wait > 30*time.Second || wait >= remaining || !c.due.Before(c.deadline) {
			return nil, ports.ErrDownloadWatchStale
		}
		return &ports.MovieUpgradeTrackingHintScheduleResult{RetryAfter: wait}, nil
	}
	added, err := s.runtime.scheduleOneWatched(ctx, c.hint, c)
	if err != nil {
		return nil, err
	}
	if !added {
		return nil, ports.ErrDownloadWatchStale
	}
	var id uuid.UUID
	if err = s.runtime.store.pool.QueryRow(ctx, `SELECT job_id FROM movie_upgrade_tracking_job WHERE operation_id=$1 AND prior_observation_id=$2`, c.request.OperationID, c.request.PriorObservationID).Scan(&id); err != nil {
		return nil, err
	}
	return &ports.MovieUpgradeTrackingHintScheduleResult{JobID: id, Queued: true}, nil
}
