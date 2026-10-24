package postgres

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AutomaticTVWatchStore struct {
	pool  *pgxpool.Pool
	ids   ports.IDGen
	clock ports.Clock
}

func NewAutomaticTVWatchStore(pool *pgxpool.Pool, ids ports.IDGen, clock ports.Clock) *AutomaticTVWatchStore {
	return &AutomaticTVWatchStore{pool: pool, ids: ids, clock: clock}
}

var _ ports.DownloadWatchRepository = (*AutomaticTVWatchStore)(nil)
var _ ports.AutomaticTVObservationHintScheduler = (*AutomaticTVWatchStore)(nil)

const automaticTVWatchSourceScope = "automatic_tv_watch_sources_v1"

func (s *AutomaticTVWatchStore) ListDownloadWatchSources(ctx context.Context, input ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error) {
	var page ports.DownloadWatchSourcePage
	n, e := watchLimit(input.Limit)
	if e != nil {
		return page, e
	}
	c, e := watchDecodeCursor(input.Cursor, automaticTVWatchSourceScope)
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
	if _, e = tx.Exec(ctx, `SET LOCAL jit=off; SET LOCAL join_collapse_limit=1; SET LOCAL from_collapse_limit=1`); e != nil {
		return page, watchError(ctx, e)
	}
	// Take the DB horizon before the projection, never from the native clock.
	var manifest time.Time
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&manifest); e != nil {
		return page, watchError(ctx, e)
	}
	rows, e := q.ListAutomaticTVWatchRoots(ctx, db.ListAutomaticTVWatchRootsParams{AfterKey: c.Key, ResumeCurrent: c.Sub != "~", PageSize: int32(n + 1)})
	if e != nil {
		return page, watchError(ctx, e)
	}
	for i, raw := range rows {
		if i == n {
			page.NextCursor = watchEncodeCursor(c)
			break
		}
		var material automaticTVWatchRootMaterial
		if e = statsDecode(raw, &material); e != nil {
			return page, ports.ErrUnavailable
		}
		r := material.watchRootMaterial
		if !material.currentClient() {
			c = watchCursor{Key: r.ClientID.String() + ":" + r.RootID.String(), Sub: "~", Scope: automaticTVWatchSourceScope}
			continue
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
			c = watchCursor{Key: key, Sub: sub, Scope: automaticTVWatchSourceScope}
		}
		if page.NextCursor != "" {
			break
		}
		c = watchCursor{Key: key, Sub: "~", Scope: automaticTVWatchSourceScope}
	}
	page.Complete = page.NextCursor == "" && len(page.Issues) == 0
	if e = tx.Commit(ctx); e != nil {
		return ports.DownloadWatchSourcePage{}, watchError(ctx, e)
	}
	return page, nil
}

type automaticTVWatchCandidate struct {
	watchTaskMaterial
	PriorJobID    uuid.UUID `json:"prior_job_id"`
	SendAttemptID uuid.UUID `json:"send_attempt_id"`
	PreparedPaths []string  `json:"prepared_paths"`
}

func automaticTVWatchPreparedPath(c automaticTVWatchCandidate, b ports.DownloadWatchBinding, h ports.RootChangeHint) bool {
	if len(c.PreparedPaths) < 1 || len(c.PreparedPaths) > 1000 {
		return false
	}
	matched := false
	for _, relative := range c.PreparedPaths {
		if !watchHint(ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: relative}) {
			return false
		}
		// Bind each full prepared path, so a deeper map cannot authorize a sibling.
		task := c.watchTaskMaterial
		task.Eligible = true
		task.ContentPath = path.Join(c.SavePath, relative)
		if watchTaskBinding(task, b, h) && path.Join(b.LocalRoot, h.RelativePath) == automaticTVWatchLocalPath(c.Root.Mapping, task.ContentPath) {
			matched = true
		}
	}
	return matched
}

func (s *AutomaticTVWatchStore) ResolveDownloadWatchTargets(ctx context.Context, b ports.DownloadWatchBinding, h ports.RootChangeHint, input ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
	var page ports.DownloadWatchTargetPage
	n, e := watchLimit(input.Limit)
	if e != nil {
		return page, e
	}
	if h.Kind != ports.RootChangeCloseWrite && h.Kind != ports.RootChangeMovedTo {
		page.Complete = true
		return page, nil
	}
	if h.Metadata == (ports.RootChangeMetadata{}) {
		page.Complete = true
		return page, nil
	}
	m := h.Metadata
	r := ports.AutomaticTVObservationHintRequest{Binding: b, Hint: ports.RootChangeHint{Kind: h.Kind, RelativePath: h.RelativePath}, SourceIdentity: m.SourceIdentity, EventID: m.EventID, InstanceID: m.InstanceID, Sequence: m.Sequence, ReceivedAt: m.ReceivedAt, ManifestAt: m.ManifestAt}
	scope := watchDigest(struct {
		Kind    string
		Binding ports.DownloadWatchBinding
		Hint    ports.RootChangeHint
	}{"automatic_tv_watch_targets_v1", b, h})
	c, e := watchDecodeCursor(input.Cursor, scope)
	if e != nil {
		return page, e
	}
	if c.Sub != "" {
		return page, ports.ErrRejected
	}
	after := uuid.Nil
	if c.Key != "" {
		after, e = uuid.Parse(c.Key)
		if e != nil || after.String() != c.Key {
			return page, ports.ErrRejected
		}
	}
	if _, e = s.remaining(r); e != nil {
		return page, e
	}
	tx, q, e := (&DownloadWatchStore{pool: s.pool}).read(ctx)
	if e != nil {
		return page, e
	}
	defer managementRollback(ctx, tx)
	if _, e = tx.Exec(ctx, `SET LOCAL jit=off; SET LOCAL join_collapse_limit=1; SET LOCAL from_collapse_limit=1`); e != nil {
		return page, watchError(ctx, e)
	}
	if e = automaticTVWatchBinding(ctx, q, b); e != nil {
		return page, e
	}
	raw, e := q.GetDownloadWatchRootMaterial(ctx, db.GetDownloadWatchRootMaterialParams{ClientID: b.ClientID, RootID: b.RootID})
	if e != nil {
		return page, watchError(ctx, e)
	}
	var root watchRootMaterial
	if e = statsDecode(raw, &root); e != nil {
		return page, ports.ErrUnavailable
	}
	rows, e := q.ListAutomaticTVWatchTasks(ctx, db.ListAutomaticTVWatchTasksParams{ClientID: b.ClientID, RootID: b.RootID, ManifestAt: pgtype.Timestamptz{Time: m.ManifestAt, Valid: true}, AfterID: after, PageSize: int32(n + 1)})
	if e != nil {
		return page, watchError(ctx, e)
	}
	for i, raw := range rows {
		if i == n {
			page.NextCursor = watchEncodeCursor(c)
			break
		}
		var candidate automaticTVWatchCandidate
		if e = statsDecode(raw, &candidate); e != nil {
			return page, ports.ErrUnavailable
		}
		c = watchCursor{Key: candidate.TaskID.String(), Scope: scope}
		candidate.Root = root
		if !automaticTVWatchPreparedPath(candidate, b, r.Hint) {
			continue
		}
		request := r
		request.OperationID = candidate.OperationID
		request.TaskID = candidate.TaskID
		request.PriorJobID = candidate.PriorJobID
		request.SendAttemptID = candidate.SendAttemptID
		capture, err := s.resolve(ctx, tx, q, request)
		if errors.Is(err, ports.ErrAutomaticTVObservationStale) || errors.Is(err, ports.ErrAutomaticTVSubmissionReview) || errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return page, automaticTVObservationError(ctx, err)
		}
		target := watchTarget(candidate.watchTaskMaterial, b)
		target.Kind = "automatic_tv_observation_hint"
		target.AutomaticTVHintCapture = capture
		page.Targets = append(page.Targets, target)
	}
	page.Complete = page.NextCursor == ""
	if e = tx.Commit(ctx); e != nil {
		return ports.DownloadWatchTargetPage{}, watchError(ctx, e)
	}
	return page, nil
}

type automaticTVWatchRootMaterial struct {
	watchRootMaterial
	ClientConfiguration struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password []byte `json:"password"`
	} `json:"client_configuration"`
	AuthorityClientStamps []string `json:"authority_client_stamps"`
}

func (r automaticTVWatchRootMaterial) currentClient() bool {
	c := db.DownloadClient{ID: r.ClientID, ClientType: r.ClientType, Url: r.ClientConfiguration.URL, Username: r.ClientConfiguration.Username, PasswordEncrypted: r.ClientConfiguration.Password, RemotePathMap: r.Mapping}
	return slices.Contains(r.AuthorityClientStamps, episodeClientStamp(c))
}
