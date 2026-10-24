package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	domain "github.com/dalamudx/PulseMedia/internal/domain/personfollow"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var _ ports.PersonFollowSyncPermit = (*PersonFollowExecution)(nil)

func (s *PersonFollowSyncStore) Acquire(ctx context.Context, lease ports.JobLease) (ports.PersonFollowSyncPermit, error) {
	execution, err := s.BeginExecution(ctx, lease)
	if err != nil {
		return nil, err
	}
	err = execution.withSourceTransaction(ctx, func(ctx context.Context, q *db.Queries) error {
		source, e := execution.readSource(ctx, q)
		if e != nil {
			return e
		}
		execution.work.SourceStored = source != nil
		return nil
	})
	if err != nil {
		execution.Close()
		return nil, err
	}
	return execution, nil
}
func personFollowSourceError(ctx context.Context, err error) error {
	if errors.Is(err, ports.ErrBadResponse) {
		return ports.ErrBadResponse
	}
	return personFollowError(ctx, err)
}
func (p *PersonFollowExecution) withSourceTransaction(ctx context.Context, action func(context.Context, *db.Queries) error) (err error) {
	defer func() { err = personFollowSourceError(ctx, err) }()
	if p == nil {
		return ports.ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ports.ErrPersonFollowConflict
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := p.conn.Begin(bounded)
	if e != nil {
		return e
	}
	defer managementRollback(bounded, tx)
	q := db.New(tx)
	if _, e = p.authorize(bounded, q); e != nil {
		return e
	}
	if e = action(bounded, q); e != nil {
		return e
	}
	if e = personFollowExecutionLease(bounded, q, p.lease); e != nil {
		return e
	}
	return tx.Commit(bounded)
}
func (p *PersonFollowExecution) readSource(ctx context.Context, q *db.Queries) (*ports.PersonCredits, error) {
	row, err := q.ReadPersonFollowSource(ctx, p.runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var source ports.PersonCredits
	if !creationStrictJSON(row.Source, &source) || source.PersonID != p.work.Config.Settings.PersonID || source.Role != p.work.Config.Settings.Role || creationDigest(source) != row.SourceHash || row.WorkCount != int32(len(source.Works)) {
		return nil, ports.ErrBadResponse
	}
	normalized, err := domain.NormalizeWorks(source.Works)
	if err != nil || !reflect.DeepEqual(normalized, source.Works) {
		return nil, ports.ErrBadResponse
	}
	return &source, nil
}

type personFollowItemInsert struct {
	ID          uuid.UUID        `json:"id"`
	RunID       uuid.UUID        `json:"run_id"`
	Ordinal     int              `json:"ordinal"`
	TMDBID      int64            `json:"tmdb_id"`
	MediaType   string           `json:"media_type"`
	Source      ports.PersonWork `json:"source"`
	RootID      *uuid.UUID       `json:"root_id"`
	CommandKey  string           `json:"command_key"`
	State       string           `json:"state"`
	ExecutionID uuid.UUID        `json:"writer_execution_id"`
}

func (p *PersonFollowExecution) StoreCredits(ctx context.Context, credits ports.PersonCredits) error {
	own := credits
	own.Works = append([]ports.PersonWork{}, credits.Works...)
	normalized, err := domain.NormalizeWorks(credits.Works)
	if err != nil {
		return ports.ErrBadResponse
	}
	own.Works = normalized
	err = p.withSourceTransaction(ctx, func(ctx context.Context, q *db.Queries) error {
		if own.PersonID != p.work.Config.Settings.PersonID || own.Role != p.work.Config.Settings.Role {
			return ports.ErrBadResponse
		}
		previous, e := p.readSource(ctx, q)
		if e != nil {
			return e
		}
		if previous != nil {
			if creationDigest(*previous) != creationDigest(own) {
				return ports.ErrPersonFollowConflict
			}
			return nil
		}
		if p.work.Start != 0 {
			return ports.ErrPersonFollowConflict
		}
		source, e := json.Marshal(own)
		if e != nil {
			return ports.ErrBadResponse
		}
		if e = q.InsertPersonFollowSource(ctx, db.InsertPersonFollowSourceParams{RunID: p.runID, ExecutionID: p.executionID, Source: source, SourceHash: creationDigest(own), WorkCount: int32(len(own.Works))}); e != nil {
			return e
		}
		inserts := make([]personFollowItemInsert, 0, len(own.Works))
		for ordinal, work := range own.Works {
			state, e := p.work.Config.Settings.Classify(work)
			if e != nil {
				return ports.ErrBadResponse
			}
			root := p.work.Config.Settings.RootFor(work.MediaType)
			var rootID *uuid.UUID
			if root != uuid.Nil {
				rootID = &root
			}
			inserts = append(inserts, personFollowItemInsert{ID: p.store.ids.NewUUID(), RunID: p.runID, Ordinal: ordinal, TMDBID: work.TMDBID, MediaType: work.MediaType, Source: work, RootID: rootID, CommandKey: domain.CreationKey(p.configID, p.work.Config.Version, root, work), State: state, ExecutionID: p.executionID})
		}
		payload, e := json.Marshal(inserts)
		if e != nil {
			return ports.ErrBadResponse
		}
		count, e := q.InsertPersonFollowItems(ctx, payload)
		if e != nil {
			return e
		}
		if count != int64(len(inserts)) {
			return ports.ErrUnavailable
		}
		return nil
	})
	if err == nil {
		p.mu.Lock()
		p.work.SourceStored = true
		p.mu.Unlock()
	}
	return err
}
func (p *PersonFollowExecution) pageItems(ctx context.Context, q *db.Queries) ([]ports.PersonFollowSyncItem, *ports.PersonCredits, error) {
	source, err := p.readSource(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	if source == nil {
		return nil, nil, ports.ErrPersonFollowConflict
	}
	start := p.work.Start
	if start > len(source.Works) || start == len(source.Works) && start != 0 {
		return nil, nil, ports.ErrBadResponse
	}
	rows, err := q.ListPersonFollowPageItems(ctx, db.ListPersonFollowPageItemsParams{RunID: p.runID, PageStart: int32(start)})
	if err != nil {
		return nil, nil, err
	}
	expected := min(ports.PersonFollowPageSize, len(source.Works)-start)
	if len(rows) != expected {
		return nil, nil, ports.ErrBadResponse
	}
	result := make([]ports.PersonFollowSyncItem, 0, len(rows))
	for i, row := range rows {
		work := source.Works[start+i]
		state, e := p.work.Config.Settings.Classify(work)
		if e != nil {
			return nil, nil, ports.ErrBadResponse
		}
		root := p.work.Config.Settings.RootFor(work.MediaType)
		observedRoot := uuid.Nil
		if row.RootID.Valid {
			observedRoot = uuid.UUID(row.RootID.Bytes)
		}
		var observation ports.PersonWork
		if row.Ordinal != int32(start+i) || row.ID == uuid.Nil || row.TmdbID != work.TMDBID || row.MediaType != work.MediaType || !creationStrictJSON(row.Source, &observation) || observation != work || root != observedRoot || row.CommandKey != domain.CreationKey(p.configID, p.work.Config.Version, root, work) {
			return nil, nil, ports.ErrBadResponse
		}
		switch row.State {
		case "pending", "created":
			if state != "pending" {
				return nil, nil, ports.ErrBadResponse
			}
		case "excluded":
			if state != "excluded" {
				return nil, nil, ports.ErrBadResponse
			}
		case "review_required":
			if state != "pending" && state != "review_required" {
				return nil, nil, ports.ErrBadResponse
			}
		default:
			return nil, nil, ports.ErrBadResponse
		}
		if row.State == "created" {
			var receipt ports.SubscriptionCreationReceipt
			if !row.SubscriptionID.Valid || !creationStrictJSON(row.Receipt, &receipt) || receipt.Subscription.ID != uuid.UUID(row.SubscriptionID.Bytes) || receipt.Subscription.UserID != p.ownerID || receipt.Subscription.RootID != root {
				return nil, nil, ports.ErrBadResponse
			}
		}
		result = append(result, ports.PersonFollowSyncItem{ID: row.ID, Observation: work, RootID: root, State: row.State, CommandKey: row.CommandKey})
	}
	return result, source, nil
}
func (p *PersonFollowExecution) Items(ctx context.Context) (result []ports.PersonFollowSyncItem, err error) {
	err = p.withSourceTransaction(ctx, func(ctx context.Context, q *db.Queries) error {
		var e error
		result, _, e = p.pageItems(ctx, q)
		return e
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (p *PersonFollowExecution) ReviewItem(ctx context.Context, id uuid.UUID) error {
	return p.withSourceTransaction(ctx, func(ctx context.Context, q *db.Queries) error {
		items, _, e := p.pageItems(ctx, q)
		if e != nil {
			return e
		}
		for _, item := range items {
			if item.ID != id {
				continue
			}
			if item.State == "review_required" {
				return nil
			}
			if item.State != "pending" {
				return ports.ErrPersonFollowConflict
			}
			count, e := q.ReviewPersonFollowItem(ctx, db.ReviewPersonFollowItemParams{ID: id, RunID: p.runID, WriterExecutionID: p.executionID})
			if e != nil {
				return e
			}
			if count != 1 {
				return ports.ErrPersonFollowConflict
			}
			return nil
		}
		return ports.ErrNotFound
	})
}
func (p *PersonFollowExecution) FinishPage(ctx context.Context) (err error) {
	defer func() { err = personFollowSourceError(ctx, err) }()
	if p == nil {
		return ports.ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ports.ErrPersonFollowConflict
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := p.conn.Begin(bounded)
	if e != nil {
		return e
	}
	defer managementRollback(bounded, tx)
	q := db.New(tx)
	if _, e = p.authorize(bounded, q); e != nil {
		return e
	}
	items, source, e := p.pageItems(bounded, q)
	if e != nil {
		return e
	}
	for _, item := range items {
		if item.State == "pending" {
			return ports.ErrPersonFollowConflict
		}
	}
	next := p.work.Start + len(items)
	if next < len(source.Works) {
		job := p.store.ids.NewUUID()
		if e = q.InsertPersonFollowSyncJob(bounded, db.InsertPersonFollowSyncJobParams{ID: job, Payload: personFollowPayloadJSON(p.runID, next)}); e != nil {
			return e
		}
		if e = q.InsertPersonFollowSyncPage(bounded, db.InsertPersonFollowSyncPageParams{JobID: job, RunID: p.runID, PageStart: int32(next)}); e != nil {
			return e
		}
	} else if e = q.CompletePersonFollowSyncRun(bounded, p.runID); e != nil {
		return e
	}
	if e = personFollowExecutionLease(bounded, q, p.lease); e != nil {
		return e
	}
	if e = q.CompletePersonFollowSyncPage(bounded, p.lease.ID); e != nil {
		return e
	}
	if e = q.CompletePersonFollowSyncJob(bounded, p.lease.ID); e != nil {
		return e
	}
	return tx.Commit(bounded)
}
