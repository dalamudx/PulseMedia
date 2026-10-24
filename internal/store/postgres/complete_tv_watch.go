package postgres

import (
	"context"
	"errors"
	"path"

	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CompleteTVWatchStore projects configured sources and captures existing partial
// tracking authority. Only ScheduleCompleteTVTrackingHint can enqueue work.
type CompleteTVWatchStore struct {
	pool      *pgxpool.Pool
	scheduler *CompleteTVTrackingRuntimeStore
	clock     ports.Clock
}

func NewCompleteTVWatchStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock) *CompleteTVWatchStore {
	return &CompleteTVWatchStore{pool: pool, scheduler: NewCompleteTVTrackingRuntimeStore(pool, ids), clock: clock}
}

var _ ports.DownloadWatchRepository = (*CompleteTVWatchStore)(nil)

const completeTVWatchSourceScope = "complete_tv_watch_sources_v1"

func (s *CompleteTVWatchStore) ListDownloadWatchSources(ctx context.Context, input ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error) {
	var page ports.DownloadWatchSourcePage
	n, e := watchLimit(input.Limit)
	if e != nil {
		return page, e
	}
	c, e := watchDecodeCursor(input.Cursor, completeTVWatchSourceScope)
	if e != nil {
		return page, e
	}
	if c.Key != "" {
		pieces := strings.Split(c.Key, ":")
		if len(pieces) != 2 {
			return page, ports.ErrRejected
		}
		for _, v := range pieces {
			id, err := uuid.Parse(v)
			if err != nil || id.String() != v {
				return page, ports.ErrRejected
			}
		}
	} else if c.Sub != "" {
		return page, ports.ErrRejected
	}
	if s == nil {
		return page, ports.ErrUnavailable
	}
	tx, q, e := (&DownloadWatchStore{pool: s.pool}).read(ctx)
	if e != nil {
		return page, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Take the DB horizon before the projection, never from the native clock.
	var manifest time.Time
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&manifest); e != nil {
		return page, watchError(ctx, e)
	}
	rows, e := q.ListCompleteTVWatchRoots(ctx, db.ListCompleteTVWatchRootsParams{AfterKey: c.Key, ResumeCurrent: c.Sub != "~", PageSize: int32(n + 1)})
	if e != nil {
		return page, watchError(ctx, e)
	}
	for i, raw := range rows {
		if i == n {
			page.NextCursor = watchEncodeCursor(c)
			break
		}
		var r watchRootMaterial
		if e = statsDecode(raw, &r); e != nil {
			return page, ports.ErrUnavailable
		}
		key := r.ClientID.String() + ":" + r.RootID.String()
		bindings, reason := watchSources(r)
		if reason != "" {
			page.Issues = append(page.Issues, ports.DownloadWatchCoverageIssue{ClientID: r.ClientID, RootID: r.RootID, Reason: reason})
		}
		for _, b := range bindings {
			sub := watchBindingKey(b)
			if key == c.Key && sub <= c.Sub {
				continue
			}
			if len(page.Sources) == n {
				page.NextCursor = watchEncodeCursor(c)
				break
			}
			page.Sources = append(page.Sources, ports.DownloadWatchSource{Binding: b, ManifestAt: manifest.UTC()})
			c = watchCursor{Key: key, Sub: sub, Scope: completeTVWatchSourceScope}
		}
		if page.NextCursor != "" {
			break
		}
		c = watchCursor{Key: key, Sub: "~", Scope: completeTVWatchSourceScope}
	}
	page.Complete = page.NextCursor == "" && len(page.Issues) == 0
	if e = tx.Commit(ctx); e != nil {
		return ports.DownloadWatchSourcePage{}, watchError(ctx, e)
	}
	return page, nil
}

type completeTVWatchCandidate struct {
	watchTaskMaterial
	PriorJobID    uuid.UUID `json:"prior_job_id"`
	PreparedPaths []string  `json:"prepared_paths"`
}

// Prepared file paths narrow a root-level SavePath; arbitrary sibling files
// cannot prompt an early observation of this torrent.
func completeTVWatchPreparedPath(c completeTVWatchCandidate, b ports.DownloadWatchBinding, h ports.RootChangeHint) bool {
	mapping, err := watchMapping(c.Root.Mapping)
	if err != nil || len(c.PreparedPaths) < 2 || len(c.PreparedPaths) > 1000 {
		return false
	}
	hinted := path.Join(b.LocalRoot, h.RelativePath)
	for _, relative := range c.PreparedPaths {
		if !watchHint(ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: relative}) {
			return false
		}
		local := naming.TranslateRemotePath(mapping, path.Join(c.SavePath, relative))
		if watchContained(b.LocalRoot, local) && local == hinted {
			return true
		}
	}
	return false
}

func (s *CompleteTVWatchStore) ResolveDownloadWatchTargets(ctx context.Context, b ports.DownloadWatchBinding, h ports.RootChangeHint, input ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
	var page ports.DownloadWatchTargetPage
	n, e := watchLimit(input.Limit)
	if e != nil {
		return page, e
	}
	// Initial registration and coverage recovery never grant early tracking.
	if h.Kind != ports.RootChangeCloseWrite && h.Kind != ports.RootChangeMovedTo {
		page.Complete = true
		return page, nil
	}
	m := h.Metadata
	if !watchHint(h) || m.EventID == uuid.Nil || m.InstanceID == uuid.Nil || m.Sequence <= 0 || m.ReceivedAt.IsZero() || m.ManifestAt.IsZero() || m.SourceIdentity.Inode <= 0 {
		return page, ports.ErrRejected
	}
	if s == nil || s.clock == nil {
		return page, ports.ErrUnavailable
	}
	scope := watchDigest(struct {
		Kind    string
		Binding ports.DownloadWatchBinding
		Hint    ports.RootChangeHint
	}{"complete_tv_watch_targets_v1", b, h})
	c, e := watchDecodeCursor(input.Cursor, scope)
	if e != nil {
		return page, e
	}
	after := uuid.Nil
	if c.Sub != "" {
		return page, ports.ErrRejected
	}
	if c.Key != "" {
		after, e = uuid.Parse(c.Key)
		if e != nil || after.String() != c.Key {
			return page, ports.ErrRejected
		}
	}
	age := s.clock.Since(m.ReceivedAt)
	if age < 0 {
		return page, ports.ErrRejected
	}
	if age > completeTVHintMaxAge {
		page.Complete = true
		return page, nil
	}
	hint := ports.RootChangeHint{Kind: h.Kind, RelativePath: h.RelativePath}
	tx, q, e := (&DownloadWatchStore{pool: s.pool}).read(ctx)
	if e != nil {
		return page, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if e = watchCurrentBinding(ctx, q, b); e != nil {
		return page, e
	}
	if b.MediaType != "tv" {
		return page, ports.ErrDownloadWatchStale
	}
	if b.LocalRoot == b.RootPath && (m.SourceIdentity.Device != b.RootIdentity.Device || m.SourceIdentity.Inode != b.RootIdentity.Inode) {
		return page, ports.ErrRejected
	}
	rawRoot, e := q.GetDownloadWatchRootMaterial(ctx, db.GetDownloadWatchRootMaterialParams{ClientID: b.ClientID, RootID: b.RootID})
	if e != nil {
		return page, watchError(ctx, e)
	}
	var root watchRootMaterial
	if e = statsDecode(rawRoot, &root); e != nil {
		return page, ports.ErrUnavailable
	}
	rows, e := q.ListCompleteTVWatchTasks(ctx, db.ListCompleteTVWatchTasksParams{ClientID: b.ClientID, RootID: b.RootID, ManifestAt: pgtype.Timestamptz{Time: m.ManifestAt, Valid: true}, AfterID: after, PageSize: int32(n + 1)})
	if e != nil {
		return page, watchError(ctx, e)
	}
	candidates := make([]completeTVWatchCandidate, 0, n)
	for i, raw := range rows {
		if i == n {
			page.NextCursor = watchEncodeCursor(c)
			break
		}
		var candidate completeTVWatchCandidate
		if e = statsDecode(raw, &candidate); e != nil {
			return page, ports.ErrUnavailable
		}
		c = watchCursor{Key: candidate.TaskID.String(), Scope: scope}
		candidate.Root = root
		// This flag enables only the shared path matcher. The dedicated SQL and
		// runtime Resolve validate COMPLETE-TV authority, never ordinary eligibility.
		candidate.Eligible = true
		if watchTaskBinding(candidate.watchTaskMaterial, b, hint) && completeTVWatchPreparedPath(candidate, b, hint) {
			candidates = append(candidates, candidate)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return ports.DownloadWatchTargetPage{}, watchError(ctx, e)
	}
	// No outer transaction/locks while Resolve validates each frozen authority.
	for _, candidate := range candidates {
		request := ports.CompleteTVTrackingHintRequest{OperationID: candidate.OperationID, TaskID: candidate.TaskID, PriorJobID: candidate.PriorJobID, HintID: m.EventID, InstanceID: m.InstanceID, Sequence: m.Sequence, Hint: hint, Binding: b, SourceIdentity: m.SourceIdentity, ReceivedAt: m.ReceivedAt}
		capture, err := s.scheduler.ResolveCompleteTVTrackingHint(ctx, request, s.clock)
		if err != nil {
			if errors.Is(err, ports.ErrCompleteTVObservationStale) || errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return ports.DownloadWatchTargetPage{}, watchError(ctx, err)
		}
		target := watchTarget(candidate.watchTaskMaterial, b)
		target.Kind = "complete_tv_tracking_hint"
		target.TrackingHintCapture = capture
		page.Targets = append(page.Targets, target)
	}
	page.Complete = page.NextCursor == ""
	return page, nil
}
