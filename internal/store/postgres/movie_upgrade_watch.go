package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

const movieUpgradeWatchSourceScope = "movie_upgrade_watch_sources_v1"

func (s *MovieUpgradeWatchStore) ListDownloadWatchSources(ctx context.Context, input ports.DownloadWatchSourceQuery) (page ports.DownloadWatchSourcePage, err error) {
	defer func() { err = movieUpgradeWatchError(ctx, err) }()
	n, err := watchLimit(input.Limit)
	if err != nil {
		return page, err
	}
	c, err := watchDecodeCursor(input.Cursor, movieUpgradeWatchSourceScope)
	if err != nil {
		return page, err
	}
	if c.Key != "" {
		pieces := strings.Split(c.Key, ":")
		if len(pieces) != 2 {
			return page, ports.ErrRejected
		}
		for _, v := range pieces {
			id, e := uuid.Parse(v)
			if e != nil || id.String() != v {
				return page, ports.ErrRejected
			}
		}
	} else if c.Sub != "" {
		return page, ports.ErrRejected
	}
	if s == nil || s.runtime == nil {
		return page, ports.ErrUnavailable
	}
	tx, q, err := (&DownloadWatchStore{pool: s.runtime.store.pool}).read(ctx)
	if err != nil {
		return page, err
	}
	defer managementRollback(ctx, tx)
	var manifest time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&manifest); err != nil {
		return page, err
	}
	rows, err := q.ListMovieUpgradeWatchRoots(ctx, db.ListMovieUpgradeWatchRootsParams{AfterKey: c.Key, ResumeCurrent: c.Sub != "~", PageSize: int32(n + 1)})
	if err != nil {
		return page, err
	}
	for i, raw := range rows {
		if i == n {
			page.NextCursor = watchEncodeCursor(c)
			break
		}
		var root watchRootMaterial
		if err = statsDecode(raw, &root); err != nil {
			return page, ports.ErrUnavailable
		}
		key := root.ClientID.String() + ":" + root.RootID.String()
		bindings, reason := watchSources(root)
		if reason != "" {
			page.Issues = append(page.Issues, ports.DownloadWatchCoverageIssue{ClientID: root.ClientID, RootID: root.RootID, Reason: reason})
		}
		for _, b := range bindings {
			sub := watchBindingKey(b)
			if key == c.Key && sub <= c.Sub {
				continue
			}
			if len(page.Sources) == n {
				page.NextCursor = watchEncodeCursor(c)
				return page, tx.Commit(ctx)
			}
			page.Sources = append(page.Sources, ports.DownloadWatchSource{Binding: b, ManifestAt: manifest.UTC()})
			c = watchCursor{Key: key, Sub: sub, Scope: movieUpgradeWatchSourceScope}
		}
		c = watchCursor{Key: key, Sub: "~", Scope: movieUpgradeWatchSourceScope}
	}
	page.Complete = page.NextCursor == "" && len(page.Issues) == 0
	return page, tx.Commit(ctx)
}

func (s *MovieUpgradeWatchStore) ResolveDownloadWatchTargets(ctx context.Context, b ports.DownloadWatchBinding, h ports.RootChangeHint, input ports.DownloadWatchTargetQuery) (page ports.DownloadWatchTargetPage, err error) {
	defer func() { err = movieUpgradeWatchError(ctx, err) }()
	n, err := watchLimit(input.Limit)
	if err != nil {
		return page, err
	}
	if h.Kind != ports.RootChangeCloseWrite && h.Kind != ports.RootChangeMovedTo {
		page.Complete = true
		return page, nil
	}
	m := h.Metadata
	r := ports.MovieUpgradeTrackingHintRequest{Binding: b, Hint: ports.RootChangeHint{Kind: h.Kind, RelativePath: h.RelativePath}, SourceIdentity: m.SourceIdentity, EventID: m.EventID, InstanceID: m.InstanceID, Sequence: m.Sequence, ReceivedAt: m.ReceivedAt, ManifestAt: m.ManifestAt}
	remaining, err := s.remaining(r)
	if err != nil {
		return page, err
	}
	// Every envelope field belongs to the cursor; UTC permits equivalent time zones.
	scopeRequest := r
	scopeRequest.Binding.ClientVersion = scopeRequest.Binding.ClientVersion.UTC()
	scopeRequest.ReceivedAt = scopeRequest.ReceivedAt.UTC()
	scopeRequest.ManifestAt = scopeRequest.ManifestAt.UTC()
	scope := watchDigest(struct {
		Kind    string
		Request ports.MovieUpgradeTrackingHintRequest
	}{"movie_upgrade_watch_targets_v1", scopeRequest})
	c, err := watchDecodeCursor(input.Cursor, scope)
	if err != nil {
		return page, err
	}
	if c.Sub != "" {
		return page, ports.ErrRejected
	}
	after := uuid.Nil
	if c.Key != "" {
		after, err = uuid.Parse(c.Key)
		if err != nil || after.String() != c.Key {
			return page, ports.ErrRejected
		}
	}
	tx, q, err := (&DownloadWatchStore{pool: s.runtime.store.pool}).read(ctx)
	if err != nil {
		return page, err
	}
	defer managementRollback(ctx, tx)
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return page, err
	}
	if m.ManifestAt.After(now) {
		return page, ports.ErrDownloadWatchStale
	}
	b.ClientVersion = b.ClientVersion.UTC()
	if err = watchCurrentBinding(ctx, q, b); err != nil {
		return page, err
	}
	raw, err := q.GetDownloadWatchRootMaterial(ctx, db.GetDownloadWatchRootMaterialParams{ClientID: b.ClientID, RootID: b.RootID})
	if err != nil {
		return page, err
	}
	var root watchRootMaterial
	if err = statsDecode(raw, &root); err != nil {
		return page, ports.ErrUnavailable
	}
	rows, err := q.ListMovieUpgradeWatchTasks(ctx, db.ListMovieUpgradeWatchTasksParams{ClientID: b.ClientID, RootID: b.RootID, ManifestAt: pgtype.Timestamptz{Time: m.ManifestAt, Valid: true}, AfterID: after, PageSize: int32(n + 1)})
	if err != nil {
		return page, err
	}
	for i, raw := range rows {
		if i == n {
			page.NextCursor = watchEncodeCursor(c)
			break
		}
		var candidate movieUpgradeWatchCandidate
		if err = statsDecode(raw, &candidate); err != nil {
			return page, ports.ErrUnavailable
		}
		c = watchCursor{Key: candidate.TaskID.String(), Scope: scope}
		candidate.Root = root
		if !movieUpgradeWatchPreparedPath(candidate, b, r.Hint) {
			continue
		}
		request := r
		request.OperationID = candidate.OperationID
		request.TaskID = candidate.TaskID
		request.PriorObservationID = candidate.PriorObservationID
		prior := candidate.PriorObservationID
		capture := &movieUpgradeWatchCapture{request: request, store: s, hint: upgradeTrackingHint{operation: request.OperationID, task: request.TaskID, actor: candidate.ActorID, prior: &prior}, digest: movieUpgradeWatchDigest(candidate), deadline: now.Add(remaining), due: candidate.Due}
		target := watchTarget(candidate.watchTaskMaterial, b)
		target.Kind = "movie_upgrade_tracking_hint"
		target.MovieUpgradeHintCapture = capture
		page.Targets = append(page.Targets, target)
	}
	if _, err = s.remaining(r); err != nil {
		return ports.DownloadWatchTargetPage{}, err
	}
	var finished time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&finished); err != nil {
		return ports.DownloadWatchTargetPage{}, err
	}
	if !finished.Before(now.Add(remaining)) {
		return ports.DownloadWatchTargetPage{}, ports.ErrDownloadWatchStale
	}
	page.Complete = page.NextCursor == ""
	return page, tx.Commit(ctx)
}
