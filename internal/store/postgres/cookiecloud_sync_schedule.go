package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CookieCloudSyncStore struct {
	pool           *pgxpool.Pool
	ids            ports.IDGen
	decoder        ports.CredentialDecoder
	protector      ports.SecretProtector
	scheduleMu     sync.Mutex
	scheduleCursor db.ListDueCookieCloudOwnersParams
	scheduleLimits *ScheduleLimits
}

func NewCookieCloudSyncStore(pool *pgxpool.Pool, ids ports.IDGen, decoder ports.CredentialDecoder, protector ports.SecretProtector, limits ...*ScheduleLimits) *CookieCloudSyncStore {
	return &CookieCloudSyncStore{pool: pool, ids: ids, decoder: decoder, protector: protector, scheduleLimits: optionalScheduleLimits(limits)}
}

type cookieCloudJobPayload struct {
	OwnerID       uuid.UUID `json:"owner_id"`
	ConfigVersion int64     `json:"config_version"`
}

func cookieCloudRuntimeGuard(ctx context.Context, q *db.Queries) error {
	isolated, err := q.GuardDoubanSyncIsolation(ctx)
	if err != nil || isolated {
		return ports.ErrUnavailable
	}
	return nil
}
func cookieCloudRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, ports.ErrForbidden, ports.ErrCookieCloudConfigConflict, ports.ErrCookieCloudSyncLeaseLost} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ports.ErrUnavailable
}
func (s *CookieCloudSyncStore) execution(ctx context.Context, q *db.Queries, owner uuid.UUID) (ports.CookieCloudExecution, error) {
	var out ports.CookieCloudExecution
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: ports.RoleAdmin}
	if err := settingUser(ctx, q, actor, true); err != nil {
		return out, err
	}
	if err := q.LockOutboxAggregate(ctx, db.LockOutboxAggregateParams{AggregateType: cookieCloudConfigAction, AggregateID: owner.String()}); err != nil {
		return out, err
	}
	row, err := q.LockCookieCloudConfig(ctx, owner)
	if err != nil {
		return out, err
	}
	view, err := cookieCloudConfigView(row)
	if err != nil {
		return out, err
	}
	if !view.Enabled {
		return out, ports.ErrCookieCloudConfigConflict
	}
	if s.decoder == nil {
		return out, ports.ErrUnavailable
	}
	raw, err := s.decoder.DecryptSecret(row.ConnectionEncrypted)
	if err != nil || len(raw) > 32768 {
		return out, ports.ErrUnavailable
	}
	defer clear(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var connection ports.CookieCloudConnection
	if decoder.Decode(&connection) != nil {
		return out, ports.ErrUnavailable
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return out, ports.ErrUnavailable
	}
	endpoint, err := url.Parse(connection.ServerURL)
	if err != nil || endpoint == nil || endpoint.User != nil || endpoint.Fragment != "" || endpoint.RawQuery != "" || endpoint.Scheme+"://"+strings.ToLower(endpoint.Host) != view.ServerOrigin || connection.UserKey == "" || connection.Password == "" {
		return out, ports.ErrUnavailable
	}
	return ports.CookieCloudExecution{Config: view, Connection: connection}, nil
}

func (s *CookieCloudSyncStore) event(ctx context.Context, q *db.Queries, owner, job uuid.UUID, version int64, action string) error {
	payload, _ := json.Marshal(struct {
		OwnerID uuid.UUID `json:"owner_id"`
		JobID   uuid.UUID `json:"job_id"`
		Version int64     `json:"configuration_version"`
	}{owner, job, version})
	if _, err := q.InsertAuditLog(ctx, db.InsertAuditLogParams{ID: s.ids.NewUUID(), ActorType: string(ports.ActorTypeSystem), ActorID: "cookiecloud-scheduler", Action: action, TargetType: "job", TargetID: job.String(), Payload: payload}); err != nil {
		return err
	}
	aggregate := db.LockOutboxAggregateParams{AggregateType: "cookiecloud.sync", AggregateID: owner.String()}
	if err := q.LockOutboxAggregate(ctx, aggregate); err != nil {
		return err
	}
	seq, err := q.GetNextAggregateSeq(ctx, db.GetNextAggregateSeqParams(aggregate))
	if err != nil {
		return err
	}
	_, err = q.InsertOutboxEvent(ctx, db.InsertOutboxEventParams{ID: s.ids.NewUUID(), AggregateType: aggregate.AggregateType, AggregateID: aggregate.AggregateID, Seq: int64(seq), EventType: action, Payload: payload})
	return err
}

// Each owner commits its own queue graph; one broken configuration cannot roll
// back healthy peers. Saving still only starts the configured waiting interval.
func (s *CookieCloudSyncStore) EnqueueDue(ctx context.Context) (err error) {
	defer func(parent context.Context) {
		if parent.Err() != nil {
			err = parent.Err()
		} else {
			err = cookieCloudRuntimeError(err)
		}
	}(ctx)
	if s == nil || s.pool == nil || s.ids == nil || s.decoder == nil || s.protector == nil {
		return ports.ErrUnavailable
	}
	if !s.scheduleMu.TryLock() {
		return nil
	}
	defer s.scheduleMu.Unlock()
	ctx, cancel := s.scheduleLimits.bound(ctx)
	defer cancel()
	q := db.New(s.pool)
	if err = cookieCloudRuntimeGuard(ctx, q); err != nil {
		return err
	}
	owners, err := q.ListDueCookieCloudOwners(ctx, s.scheduleCursor)
	if err != nil {
		return err
	}
	if len(owners) == 0 && s.scheduleCursor.AfterOwnerID != uuid.Nil {
		s.scheduleCursor = db.ListDueCookieCloudOwnersParams{}
		owners, err = q.ListDueCookieCloudOwners(ctx, s.scheduleCursor)
		if err != nil {
			return err
		}
	}
	var firstErr error
	for _, owner := range owners {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if candidateErr := s.scheduleLimits.run(ctx, owner.OwnerID, owner.Version, func() error { return s.enqueueOwner(ctx, owner.OwnerID) }); candidateErr != nil && firstErr == nil {
			firstErr = candidateErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.scheduleCursor = db.ListDueCookieCloudOwnersParams{AfterOwnerID: owner.OwnerID, AfterScanAt: owner.ScanAt}
	}
	if len(owners) < 64 {
		s.scheduleCursor = db.ListDueCookieCloudOwnersParams{}
	}
	return firstErr
}

func (s *CookieCloudSyncStore) enqueueOwner(ctx context.Context, owner uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = cookieCloudRuntimeGuard(ctx, q); err != nil {
		return err
	}
	frozen, err := s.execution(ctx, q, owner)
	if errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrCookieCloudConfigConflict) || errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	schedule, err := q.GetCookieCloudSyncSchedule(ctx, owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	now, err := q.GetSearchDatabaseTime(ctx)
	if err != nil {
		return err
	}
	due := frozen.Config.UpdatedAt.Add(time.Duration(frozen.Config.IntervalSeconds) * time.Second)
	if schedule.OwnerID != uuid.Nil {
		if schedule.State != "idle" || schedule.ActiveJobID.Valid {
			return nil
		}
		if schedule.ConfigVersion == frozen.Config.Version {
			due = schedule.NextDueAt.Time
		}
	}
	if !now.Valid || due.After(now.Time) {
		return nil
	}
	payload, _ := json.Marshal(cookieCloudJobPayload{owner, frozen.Config.Version})
	job, err := q.CreateCookieCloudSyncJob(ctx, db.CreateCookieCloudSyncJobParams{ID: s.ids.NewUUID(), DedupeKey: pgtype.Text{String: fmt.Sprintf("%s:%d", owner, frozen.Config.Version), Valid: true}, Payload: payload})
	if err != nil {
		return err
	}
	if err = q.CreateCookieCloudSyncAttempt(ctx, db.CreateCookieCloudSyncAttemptParams{JobID: job.ID, OwnerID: owner, ConfigVersion: frozen.Config.Version}); err != nil {
		return err
	}
	if err = q.QueueCookieCloudSyncSchedule(ctx, db.QueueCookieCloudSyncScheduleParams{OwnerID: owner, ConfigVersion: frozen.Config.Version, ActiveJobID: pgtype.UUID{Bytes: job.ID, Valid: true}}); err != nil {
		return err
	}
	if err = s.event(ctx, q, owner, job.ID, frozen.Config.Version, "cookiecloud.sync_queued"); err != nil {
		return err
	}
	if err = cookieCloudRuntimeGuard(ctx, q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecoverExpired never resumes native I/O. A held physical gate retains the
// original attempt. With no provable call ownership, review protection persists
// across configuration changes instead of creating another execution.
func (s *CookieCloudSyncStore) RecoverExpired(ctx context.Context) (err error) {
	defer func() { err = cookieCloudRuntimeError(err) }()
	if s == nil || s.pool == nil || s.ids == nil {
		return ports.ErrUnavailable
	}
	conn, err := AcquirePhysicalConnection(ctx, s.pool)
	if err != nil {
		return err
	}
	defer conn.Release()
	var held []uuid.UUID
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		for _, owner := range held {
			released, e := db.New(conn).ReleaseCookieCloudSyncGate(releaseCtx, owner)
			if e != nil || !released {
				_ = conn.Conn().Close(releaseCtx)
				return
			}
		}
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer managementRollback(ctx, tx)
	q := db.New(tx)
	if err = cookieCloudRuntimeGuard(ctx, q); err != nil {
		return err
	}
	attempts, err := q.ListExpiredCookieCloudSync(ctx)
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		owned, e := q.TryCookieCloudSyncGate(ctx, attempt.OwnerID)
		if e != nil {
			return e
		}
		if !owned {
			continue
		}
		held = append(held, attempt.OwnerID)
		if e = q.ReviewCookieCloudSyncAttempt(ctx, attempt.JobID); e != nil {
			return e
		}
		if e = q.ReviewCookieCloudSyncSchedule(ctx, pgtype.UUID{Bytes: attempt.JobID, Valid: true}); e != nil {
			return e
		}
		if e = q.ReviewCookieCloudSyncJob(ctx, attempt.JobID); e != nil {
			return e
		}
		if e = s.event(ctx, q, attempt.OwnerID, attempt.JobID, attempt.ConfigVersion, "cookiecloud.sync_review_required"); e != nil {
			return e
		}
	}
	if err = cookieCloudRuntimeGuard(ctx, q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
