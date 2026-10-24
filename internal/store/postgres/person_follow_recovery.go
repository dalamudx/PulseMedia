package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

var _ ports.PersonFollowSyncRepository = (*PersonFollowSyncStore)(nil)

func (s *PersonFollowSyncStore) RecoverExpired(ctx context.Context) (err error) {
	defer func() { err = personFollowError(ctx, err) }()
	if s == nil || s.pool == nil {
		return ports.ErrUnavailable
	}
	after := uuid.Nil
	for {
		candidates, e := db.New(s.pool).ListRecoverablePersonFollowJobs(ctx, after)
		if e != nil {
			return e
		}
		for _, candidate := range candidates {
			if e = s.recover(ctx, candidate); e != nil {
				return e
			}
			after = candidate.ID
		}
		if len(candidates) < ports.PersonFollowPageSize {
			return nil
		}
	}
}
func (s *PersonFollowSyncStore) recover(ctx context.Context, hint db.ListRecoverablePersonFollowJobsRow) error {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(bounded)
	if err != nil {
		return err
	}
	defer managementRollback(bounded, tx)
	q := db.New(tx)
	isolated, err := q.GuardDoubanSyncIsolation(bounded)
	if err != nil {
		return err
	}
	if isolated {
		return ports.ErrUnavailable
	}
	held, err := q.TryPersonFollowRecoveryLock(bounded, hint.ConfigID)
	if err != nil || !held {
		return err
	}
	user, err := q.LockPersonFollowOwner(bounded, hint.OwnerID)
	if err != nil {
		return err
	}
	config, err := q.LockPersonFollowConfig(bounded, hint.ConfigID)
	if err != nil {
		return err
	}
	run, err := q.LockPersonFollowRun(bounded, hint.RunID)
	if err != nil {
		return err
	}
	job, err := q.LockPersonFollowJob(bounded, hint.ID)
	if err != nil {
		return err
	}
	page, err := q.LockPersonFollowPage(bounded, hint.ID)
	if err != nil {
		return err
	}
	if run.ConfigID != config.ID || run.OwnerID != hint.OwnerID || page.RunID != run.ID || page.Finished || (run.Status != "queued" && run.Status != "running") {
		return nil
	}
	if job.Kind != ports.PersonFollowSyncJobKind {
		return ports.ErrPersonFollowConflict
	}
	if job.State == "running" {
		valid, e := q.PersonFollowLeaseValid(bounded, db.PersonFollowLeaseValidParams{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken})
		if e != nil {
			return e
		}
		if valid.Valid && valid.Bool {
			return nil
		}
	}
	unreleased, err := q.PersonFollowUnreleasedExecution(bounded, job.ID)
	if err != nil {
		return err
	}
	next, reason, terminal := "queued", "person follow released execution may resume", ""
	if unreleased || job.Attempts >= job.MaxAttempts || job.State == "succeeded" {
		next, reason, terminal = "dead", "person follow physical outcome requires review", "review_required"
	} else {
		authorized := user.IsActive && (user.Role == ports.RoleAdmin || user.Role == ports.RoleMember) && config.Enabled && config.OwnerID == run.OwnerID && config.Version == run.ConfigVersion
		if authorized {
			current, e := personFollowSnapshot(bounded, q, config)
			if e != nil {
				return e
			}
			var frozen ports.PersonFollowConfig
			authorized = creationStrictJSON(run.ConfigSnapshot, &frozen) && personFollowSameConfig(current, frozen)
			if authorized {
				e = personFollowTargets(bounded, q, current.Settings)
				if errors.Is(e, ports.ErrNotFound) || errors.Is(e, ports.ErrRejected) || errors.Is(e, ports.ErrPersonFollowConflict) {
					authorized = false
				} else if e != nil {
					return e
				}
			}
		}
		if !authorized {
			next, reason, terminal = "dead", "person follow configuration or authority changed", "aborted"
		}
	}
	if job.State == "queued" && next == "queued" {
		return nil
	}
	count, err := q.RecoverPersonFollowJob(bounded, db.RecoverPersonFollowJobParams{ID: job.ID, NextState: next, Reason: reason, PreviousState: job.State, LeaseToken: job.LeaseToken})
	if err != nil {
		return err
	}
	if count != 1 {
		return ports.ErrPersonFollowConflict
	}
	if terminal != "" {
		count, err = q.RecoverPersonFollowRun(bounded, db.RecoverPersonFollowRunParams{ID: run.ID, NextState: terminal})
		if err != nil {
			return err
		}
		if count != 1 {
			return ports.ErrPersonFollowConflict
		}
	}
	return tx.Commit(bounded)
}
