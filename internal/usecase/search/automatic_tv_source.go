package search

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// SetAutomaticTVRoundRepository installs the source lifecycle repository at assembly time.
func (s *Service) SetAutomaticTVRoundRepository(repo ports.AutomaticTVRoundRepository) {
	s.automaticTVStore = repo
}

// ExecuteAutomaticTVSource observes one frozen episode; it grants no acquisition authority.
// The caller owns job renewal and waits for this synchronous execution to return.
func (s *Service) ExecuteAutomaticTVSource(ctx context.Context, task ports.AutomaticTVSourceTask) error {
	if !validAutomaticTVTask(task) {
		return ports.ErrBadResponse
	}
	if s.automaticTVStore == nil {
		return ports.ErrUnavailable
	}
	loaded, err := s.automaticTVStore.LoadAutomaticTVSource(ctx, task)
	if err != nil {
		return automaticTVSafeError(err)
	}
	if loaded == nil {
		return ports.ErrBadResponse
	}
	work := cloneAutomaticTVWork(*loaded)
	if work.Task != task {
		return ports.ErrBadResponse
	}
	outcome := ports.AutomaticTVSourceFailed
	if err = validateAutomaticTVWork(work, task); err == nil {
		outcome, err = s.runAutomaticTVSource(ctx, work)
	}
	// Only the repository can finish the original attempt. Never replace its lease,
	// append local query IDs, or advance NextPage to manufacture write authority.
	finishCtx, cancel := automaticTVTerminalContext(ctx)
	defer cancel()
	if finishErr := s.automaticTVStore.FinishAutomaticTVSource(finishCtx, cloneAutomaticTVWork(work), outcome); finishErr != nil {
		return automaticTVSafeError(finishErr)
	}
	return automaticTVSafeError(err)
}

func (s *Service) runAutomaticTVSource(ctx context.Context, work ports.AutomaticTVSourceWork) (ports.AutomaticTVSourceOutcome, error) {
	deadline := minAutomaticTVDeadline(work.Deadline, time.Now().Add(aggregateContinuationTimeout))
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	committed := len(work.SavedQueries)
	stop := func(err error) (ports.AutomaticTVSourceOutcome, error) {
		outcome := ports.AutomaticTVSourceFailed
		switch {
		case committed > 0:
			outcome = ports.AutomaticTVSourcePartial
		case errors.Is(err, ports.ErrAutomaticTVSearchDeferred):
			outcome = ports.AutomaticTVSourceDeferred
		case errors.Is(err, ports.ErrTimeout), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			outcome = ports.AutomaticTVSourceTimeout
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return outcome, automaticTVSafeError(err)
	}
	chain := pageChain{seen: make(map[string]bool)}
	for page := int32(0); page < aggregatePageLimit; page++ {
		if err := callCtx.Err(); err != nil {
			return stop(err)
		}
		var queryID uuid.UUID
		if page < work.NextPage {
			queryID = work.SavedQueries[page]
		} else {
			if page > 0 {
				if err := s.waitAutomaticTVContinuation(callCtx, work); err != nil {
					return stop(err)
				}
			}
			query := work.Query
			query.Offset = page * 50
			canonical, _, hash, err := CanonicalTVEpisodeQuery(query)
			if err != nil {
				return stop(err)
			}
			binding := ports.AutomaticTVQueryBinding{Task: work.Task, SnapshotHash: work.SnapshotHash, Query: canonical, ParametersHash: hash}
			var previous *pageChain
			if page > 0 {
				previous = &chain
			}
			result, err := s.executeCore(callCtx, work.Task.IndexerID, TVEpisodeQuerySchemaVersion+":"+hash, page, uuid.Nil, work.ConfigVersion, nil, false, previous, nil, &binding)
			if err != nil {
				return stop(err)
			}
			if result == nil || result.State != "succeeded" || result.QueryID == uuid.Nil {
				return stop(ports.ErrBadResponse)
			}
			queryID = result.QueryID
			committed++
		}
		proof, err := s.repo.ReadPage(callCtx, queryID)
		if err != nil {
			return stop(err)
		}
		// A short unproven page is useful observation, never proof of exhaustion.
		if proof.QueryID != queryID || proof.PageOffset != int64(page)*50 || proof.PageLimit != 50 || proof.ItemCount < 0 || proof.ItemCount > 50 || !proof.OffsetVerified || proof.Total == nil || *proof.Total < 0 {
			return stop(ports.ErrBadResponse)
		}
		total := *proof.Total
		end := proof.PageOffset + int64(proof.ItemCount)
		if (proof.ItemCount > 0 && (proof.PageOffset >= total || end > total)) || (proof.ItemCount == 0 && proof.PageOffset < total) || (page > 0 && total != chain.total) {
			return stop(ports.ErrBadResponse)
		}
		rows, err := s.repo.ReadQueryCandidates(callCtx, queryID)
		if err != nil {
			return stop(err)
		}
		if len(rows) != int(proof.ItemCount) {
			return stop(ports.ErrBadResponse)
		}
		for _, row := range rows {
			if row.Guid == "" || chain.seen[row.Guid] {
				return stop(ports.ErrBadResponse)
			}
			chain.seen[row.Guid] = true
		}
		chain.total = total
		if end >= total {
			if page+1 < work.NextPage {
				return stop(ports.ErrBadResponse)
			}
			if err := callCtx.Err(); err != nil {
				return stop(err)
			}
			return ports.AutomaticTVSourceSucceeded, nil
		}
		if proof.ItemCount != 50 {
			return stop(ports.ErrBadResponse)
		}
	}
	return ports.AutomaticTVSourcePartial, nil
}

func (s *Service) waitAutomaticTVContinuation(ctx context.Context, work ports.AutomaticTVSourceWork) error {
	delay, err := s.repo.ContinuationDelay(ctx, work.Task.IndexerID, work.ConfigVersion)
	if err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if delay < 0 {
		return ports.ErrBadResponse
	}
	if !ok || time.Until(deadline) <= delay {
		return ports.ErrAutomaticTVSearchDeferred
	}
	if delay == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func validAutomaticTVTask(task ports.AutomaticTVSourceTask) bool {
	return task.RoundID != uuid.Nil && task.IndexerID != uuid.Nil && task.Lease.ID != uuid.Nil && task.Lease.Owner != "" && task.Lease.Token > 0
}

func validateAutomaticTVWork(work ports.AutomaticTVSourceWork, task ports.AutomaticTVSourceTask) error {
	if work.Task != task || !validAutomaticTVTask(work.Task) || work.ConfigVersion <= 0 || work.Deadline.IsZero() || !validAutomaticTVHash(work.SnapshotHash) || work.NextPage < 0 || work.NextPage > aggregatePageLimit || len(work.SavedQueries) != int(work.NextPage) || work.Query.Offset != 0 {
		return ports.ErrBadResponse
	}
	if _, _, _, err := CanonicalTVEpisodeQuery(work.Query); err != nil {
		return err
	}
	seen := make(map[uuid.UUID]bool)
	for _, id := range work.SavedQueries {
		if id == uuid.Nil || seen[id] {
			return ports.ErrBadResponse
		}
		seen[id] = true
	}
	return nil
}

func freezeAutomaticTVBinding(binding ports.AutomaticTVQueryBinding, indexer uuid.UUID, keyword string, page int32) (ports.AutomaticTVQueryBinding, error) {
	query, _, hash, err := CanonicalTVEpisodeQuery(binding.Query)
	if err != nil || !validAutomaticTVTask(binding.Task) || binding.Task.IndexerID != indexer || !validAutomaticTVHash(binding.SnapshotHash) || page < 0 || page >= aggregatePageLimit || query.Offset != page*50 || binding.ParametersHash != hash || keyword != TVEpisodeQuerySchemaVersion+":"+hash {
		return ports.AutomaticTVQueryBinding{}, ports.ErrBadResponse
	}
	binding.Query = query
	return binding, nil
}

func validateAutomaticTVCategories(query ports.TVEpisodeQuery, categories []int32) error {
	configured := query
	configured.Categories = categories
	_, _, expected, err := CanonicalTVEpisodeQuery(query)
	if err != nil {
		return err
	}
	_, _, actual, err := CanonicalTVEpisodeQuery(configured)
	if err != nil || actual != expected {
		return ports.ErrBadResponse
	}
	return nil
}

func cloneAutomaticTVWork(work ports.AutomaticTVSourceWork) ports.AutomaticTVSourceWork {
	work.Query.Categories = slices.Clone(work.Query.Categories)
	work.SavedQueries = slices.Clone(work.SavedQueries)
	return work
}

func cloneAutomaticTVBinding(binding *ports.AutomaticTVQueryBinding) *ports.AutomaticTVQueryBinding {
	if binding == nil {
		return nil
	}
	cloned := *binding
	cloned.Query.Categories = slices.Clone(binding.Query.Categories)
	return &cloned
}

func validAutomaticTVHash(hash string) bool {
	decoded, err := hex.DecodeString(hash)
	return err == nil && len(decoded) == 32
}

func minAutomaticTVDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func automaticTVTerminalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() != nil {
		return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	}
	return ctx, func() {}
}

func failAutomaticTVQuery(ctx context.Context, permit ports.SearchExecutionPermit, failure ports.SearchQueryFailure, retry time.Duration) error {
	terminalCtx, cancel := automaticTVTerminalContext(ctx)
	defer cancel()
	return permit.FailQuery(terminalCtx, failure, retry)
}

func automaticTVFirstError(first, next error) error {
	if first != nil {
		return automaticTVSafeError(first)
	}
	return automaticTVSafeError(next)
}

// Adapter and repository text can contain provider URLs and secrets.
func automaticTVSafeError(err error) error {
	if err == nil {
		return nil
	}
	for _, safe := range []error{context.Canceled, ports.ErrTimeout, ports.ErrAutomaticTVSearchDeferred, ports.ErrBadResponse, ports.ErrRejected, ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrUserDisabled, ports.ErrStaleDeliveryLease, ports.ErrAuthRequired, ports.ErrUnavailable} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ports.ErrTimeout
	}
	return ports.ErrUnavailable
}
