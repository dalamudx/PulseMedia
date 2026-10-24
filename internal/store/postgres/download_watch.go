package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Watch sources are read projections. Only the existing download transaction
// can turn an independently confirmed completion into an organization job.
type DownloadWatchStore struct{ pool *pgxpool.Pool }

func NewDownloadWatchStore(pool *pgxpool.Pool) *DownloadWatchStore {
	return &DownloadWatchStore{pool: pool}
}

var _ ports.DownloadWatchRepository = (*DownloadWatchStore)(nil)

type watchRootMaterial struct {
	ClientID          uuid.UUID       `json:"client_id"`
	ClientVersion     time.Time       `json:"client_version"`
	ClientStamp       string          `json:"client_stamp"`
	Mapping           json.RawMessage `json:"mapping"`
	ClientType        string          `json:"client_type"`
	RootID            uuid.UUID       `json:"root_id"`
	RootPath          string          `json:"root_path"`
	Generation        int64           `json:"generation"`
	MediaType         string          `json:"media_type"`
	Marker            string          `json:"marker"`
	Online            bool            `json:"online"`
	BaselineCompleted bool            `json:"baseline_completed"`
	BaselineID        uuid.UUID       `json:"baseline_id"`
	Device            int64           `json:"device"`
	Inode             int64           `json:"inode"`
	BaselinePath      string          `json:"baseline_path"`
	BaselineMediaType string          `json:"baseline_media_type"`
	BaselineMarker    string          `json:"baseline_marker"`
}
type watchTaskMaterial struct {
	TaskID      uuid.UUID         `json:"task_id"`
	OperationID uuid.UUID         `json:"operation_id"`
	ClientID    uuid.UUID         `json:"client_id"`
	State       string            `json:"state"`
	SavePath    string            `json:"save_path"`
	ContentPath string            `json:"content_path"`
	Eligible    bool              `json:"eligible"`
	TaskStamp   string            `json:"task_stamp"`
	Root        watchRootMaterial `json:"root"`
}
type watchCursor struct {
	Key   string `json:"key"`
	Sub   string `json:"sub"`
	Scope string `json:"scope"`
}

func watchDigest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func watchEncodeCursor(c watchCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func watchDecodeCursor(raw, scope string) (watchCursor, error) {
	if raw == "" {
		return watchCursor{Scope: scope}, nil
	}
	if len(raw) > 8192 {
		return watchCursor{}, ports.ErrRejected
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return watchCursor{}, ports.ErrRejected
	}
	var c watchCursor
	if e = statsDecode(b, &c); e != nil || c.Scope != scope || watchEncodeCursor(c) != raw {
		return watchCursor{}, ports.ErrRejected
	}
	return c, nil
}
func watchLimit(n int) (int, error) {
	if n == 0 {
		return 64, nil
	}
	if n < 1 || n > 256 {
		return 0, ports.ErrRejected
	}
	return n, nil
}
func watchPath(p string) bool {
	if p == "" || len(p) > 4096 || !strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.Contains(p, "\\") {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func watchContained(root, p string) bool {
	return p == root || (root == "/" && strings.HasPrefix(p, "/")) || strings.HasPrefix(p, root+"/")
}
func watchMapping(raw []byte) (map[string]string, error) {
	if len(raw) == 0 || len(raw) > 65536 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, ports.ErrRejected
	}
	var fields map[string]json.RawMessage
	if e := statsDecode(raw, &fields); e != nil || fields == nil || len(fields) > 64 {
		return nil, ports.ErrRejected
	}
	result := make(map[string]string, len(fields))
	for remote, v := range fields {
		var local string
		if !watchPath(remote) || json.Unmarshal(v, &local) != nil || !watchPath(local) || local == "/" {
			return nil, ports.ErrRejected
		}
		result[remote] = local
	}
	return result, nil
}
func watchBindingKey(b ports.DownloadWatchBinding) string { return b.SourceKind + ":" + b.RemotePrefix }
func watchSources(r watchRootMaterial) ([]ports.DownloadWatchBinding, string) {
	mapping, e := watchMapping(r.Mapping)
	if e != nil {
		return nil, "invalid_mapping"
	}
	if r.ClientID == uuid.Nil || r.RootID == uuid.Nil || r.ClientVersion.IsZero() || len(r.ClientStamp) != 64 || (r.ClientType != "qbittorrent" && r.ClientType != "transmission" && r.ClientType != "rtorrent") {
		return nil, "invalid_client"
	}
	if !watchPath(r.RootPath) || r.RootPath == "/" || r.Generation < 1 || (r.MediaType != "movie" && r.MediaType != "tv") {
		return nil, "invalid_library_root"
	}
	if !r.Online {
		return nil, "root_unavailable"
	}
	if !r.BaselineCompleted || r.BaselineID == uuid.Nil || r.Inode == 0 {
		return nil, "baseline_unavailable"
	}
	if r.BaselinePath != r.RootPath || r.BaselineMediaType != r.MediaType || r.BaselineMarker != r.Marker {
		return nil, "baseline_mismatch"
	}
	base := ports.DownloadWatchBinding{ClientID: r.ClientID, ClientVersion: r.ClientVersion.UTC(), ClientStamp: r.ClientStamp, RootID: r.RootID, Generation: r.Generation, RootPath: r.RootPath, MediaType: r.MediaType, MarkerFilename: r.Marker, BaselineScanID: r.BaselineID, RootIdentity: ports.FileIdentity{Device: r.Device, Inode: r.Inode}}
	keys := make([]string, 0, len(mapping))
	for remote := range mapping {
		keys = append(keys, remote)
	}
	sort.Strings(keys)
	bindings := make([]ports.DownloadWatchBinding, 0, len(keys)+1)
	// Library identity grants only this explicitly configured library path. It
	// does not declare a remote client local merely because its mapping is empty.
	identity := base
	identity.SourceKind = "library_identity"
	identity.LocalRoot = r.RootPath
	identity.Stamp = watchDigest(identity)
	bindings = append(bindings, identity)
	for _, remote := range keys {
		b := base
		b.SourceKind = "path_mapping"
		b.RemotePrefix = remote
		b.LocalRoot = mapping[remote]
		b.Stamp = watchDigest(b)
		bindings = append(bindings, b)
	}
	sort.Slice(bindings, func(i, j int) bool { return watchBindingKey(bindings[i]) < watchBindingKey(bindings[j]) })
	return bindings, ""
}
func watchError(ctx context.Context, e error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(e, ports.ErrRejected) || errors.Is(e, ports.ErrDownloadWatchStale) || errors.Is(e, ErrRestoreIsolated) {
		return e
	}
	return ports.ErrUnavailable
}
func watchGuard(ctx context.Context, q *db.Queries) error {
	isolated, e := q.GuardDownloadWatchIsolation(ctx)
	if e != nil {
		return watchError(ctx, e)
	}
	if isolated {
		return ErrRestoreIsolated
	}
	return ctx.Err()
}
func (s *DownloadWatchStore) read(ctx context.Context) (pgx.Tx, *db.Queries, error) {
	if s == nil || s.pool == nil {
		return nil, nil, ports.ErrUnavailable
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, nil, watchError(ctx, e)
	}
	q := db.New(tx)
	if e = watchGuard(ctx, q); e != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return nil, nil, e
	}
	return tx, q, nil
}
func (s *DownloadWatchStore) ListDownloadWatchSources(ctx context.Context, input ports.DownloadWatchSourceQuery) (ports.DownloadWatchSourcePage, error) {
	var page ports.DownloadWatchSourcePage
	n, e := watchLimit(input.Limit)
	if e != nil {
		return page, e
	}
	c, e := watchDecodeCursor(input.Cursor, "sources")
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
	}
	tx, q, e := s.read(ctx)
	if e != nil {
		return page, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, e := q.ListDownloadWatchRootMaterial(ctx, db.ListDownloadWatchRootMaterialParams{AfterKey: c.Key, ResumeCurrent: c.Sub != "~", PageSize: int32(n + 1)})
	if e != nil {
		return page, watchError(ctx, e)
	}
	for index, raw := range rows {
		if index == n {
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
			page.Sources = append(page.Sources, ports.DownloadWatchSource{Binding: b})
			c = watchCursor{Key: key, Sub: sub, Scope: "sources"}
		}
		if page.NextCursor != "" {
			break
		}
		c = watchCursor{Key: key, Sub: "~", Scope: "sources"}
	}
	page.Complete = page.NextCursor == "" && len(page.Issues) == 0
	if e = tx.Commit(ctx); e != nil {
		return ports.DownloadWatchSourcePage{}, watchError(ctx, e)
	}
	return page, nil
}
func watchHint(h ports.RootChangeHint) bool {
	if h.Kind != ports.RootChangeCloseWrite && h.Kind != ports.RootChangeMovedTo && h.Kind != ports.RootChangeRescan {
		return false
	}
	p := h.RelativePath
	if p == "." {
		return h.Kind == ports.RootChangeRescan
	}
	if p == "" || len(p) > 4096 || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.HasPrefix(p, "../") || p == ".." || strings.Contains(p, "\\") {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func watchTaskBinding(t watchTaskMaterial, b ports.DownloadWatchBinding, h ports.RootChangeHint) bool {
	if !t.Eligible || !watchPath(t.SavePath) {
		return false
	}
	mapping, e := watchMapping(t.Root.Mapping)
	if e != nil {
		return false
	}
	remote := t.SavePath
	if t.ContentPath != "" {
		if !watchPath(t.ContentPath) || !watchContained(t.SavePath, t.ContentPath) {
			return false
		}
		remote = t.ContentPath
	}
	translated := naming.TranslateRemotePath(mapping, remote)
	if !watchPath(translated) || !watchContained(b.LocalRoot, translated) {
		return false
	}
	switch b.SourceKind {
	case "path_mapping":
		longest := ""
		for prefix := range mapping {
			if watchContained(prefix, remote) && len(prefix) > len(longest) {
				longest = prefix
			}
		}
		if longest != b.RemotePrefix || mapping[longest] != b.LocalRoot {
			return false
		}
	case "library_identity":
		if translated != remote || b.LocalRoot != b.RootPath {
			return false
		}
	default:
		return false
	}
	if h.RelativePath == "." {
		return true
	}
	hinted := path.Join(b.LocalRoot, h.RelativePath)
	if hinted == translated || watchContained(translated, hinted) {
		return true
	}
	return h.Kind == ports.RootChangeRescan && watchContained(hinted, translated)
}
func watchTarget(t watchTaskMaterial, b ports.DownloadWatchBinding) ports.DownloadWatchTarget {
	return ports.DownloadWatchTarget{Binding: b, TaskID: t.TaskID, OperationID: t.OperationID, ClientID: t.ClientID, ClientVersion: t.Root.ClientVersion.UTC(), ClientStamp: t.Root.ClientStamp, TaskStamp: t.TaskStamp, Kind: "ordinary"}
}
func watchCurrentBinding(ctx context.Context, q *db.Queries, b ports.DownloadWatchBinding) error {
	raw, e := q.GetDownloadWatchRootMaterial(ctx, db.GetDownloadWatchRootMaterialParams{ClientID: b.ClientID, RootID: b.RootID})
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return ports.ErrDownloadWatchStale
		}
		return watchError(ctx, e)
	}
	var root watchRootMaterial
	if e = statsDecode(raw, &root); e != nil {
		return ports.ErrUnavailable
	}
	bindings, _ := watchSources(root)
	for _, current := range bindings {
		if current == b {
			return nil
		}
	}
	return ports.ErrDownloadWatchStale
}
func (s *DownloadWatchStore) ResolveDownloadWatchTargets(ctx context.Context, b ports.DownloadWatchBinding, h ports.RootChangeHint, input ports.DownloadWatchTargetQuery) (ports.DownloadWatchTargetPage, error) {
	var page ports.DownloadWatchTargetPage
	if !watchHint(h) {
		return page, ports.ErrRejected
	}
	n, e := watchLimit(input.Limit)
	if e != nil {
		return page, e
	}
	scope := watchDigest([]string{b.Stamp, string(h.Kind), h.RelativePath})
	c, e := watchDecodeCursor(input.Cursor, scope)
	if e != nil {
		return page, e
	}
	after := uuid.Nil
	if c.Key != "" {
		after, e = uuid.Parse(c.Key)
		if e != nil || after.String() != c.Key || c.Sub != "" {
			return page, ports.ErrRejected
		}
	}
	tx, q, e := s.read(ctx)
	if e != nil {
		return page, e
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if e = watchCurrentBinding(ctx, q, b); e != nil {
		return page, e
	}
	// At most one bounded database batch per call; NextCursor also advances over
	// nonmatching paths so unrelated tasks cannot hide the tail of a large root.
	rows, e := q.ListDownloadWatchTaskMaterial(ctx, db.ListDownloadWatchTaskMaterialParams{ClientID: b.ClientID, RootID: b.RootID, AfterID: after, PageSize: int32(n + 1)})
	if e != nil {
		return page, watchError(ctx, e)
	}
	for index, raw := range rows {
		if index == n {
			page.NextCursor = watchEncodeCursor(c)
			break
		}
		var t watchTaskMaterial
		if e = statsDecode(raw, &t); e != nil {
			return page, ports.ErrUnavailable
		}
		c = watchCursor{Key: t.TaskID.String(), Scope: scope}
		if watchTaskBinding(t, b, h) {
			page.Targets = append(page.Targets, watchTarget(t, b))
		}
	}
	page.Complete = page.NextCursor == ""
	if e = tx.Commit(ctx); e != nil {
		return ports.DownloadWatchTargetPage{}, watchError(ctx, e)
	}
	return page, nil
}

// Both validators use the original authority stamp. The tail accepts an
// explicitly returned observation row, never a newly authorized target.
func ValidateDownloadWatchTarget(ctx context.Context, q *db.Queries, target ports.DownloadWatchTarget, lock bool) error {
	if e := watchGuard(ctx, q); e != nil {
		return e
	}
	if target.Kind != "ordinary" || target.TaskID == uuid.Nil || target.OperationID == uuid.Nil || target.ClientID != target.Binding.ClientID {
		return ports.ErrDownloadWatchStale
	}
	if lock {
		if _, e := q.LockDownloadWatchRoot(ctx, target.Binding.RootID); e != nil {
			return watchError(ctx, e)
		}
		if _, e := q.LockDownloadWatchMedia(ctx, target.OperationID); e != nil {
			return watchError(ctx, e)
		}
		if _, e := q.LockDownloadWatchUnits(ctx, target.OperationID); e != nil {
			return watchError(ctx, e)
		}
		if e := q.LockDownloadAcquisition(ctx, target.OperationID); e != nil {
			return watchError(ctx, e)
		}
		if _, e := q.LockDownloadClientForEdit(ctx, target.ClientID); e != nil {
			return watchError(ctx, e)
		}
		if _, e := q.LockDownloadTask(ctx, target.TaskID); e != nil {
			return watchError(ctx, e)
		}
		if _, e := q.LockDownloadWatchDemands(ctx, target.OperationID); e != nil {
			return watchError(ctx, e)
		}
	}
	_, e := watchValidateMaterial(ctx, q, target, true)
	return e
}
func watchValidateMaterial(ctx context.Context, q *db.Queries, target ports.DownloadWatchTarget, tracking bool) (watchTaskMaterial, error) {
	var t watchTaskMaterial
	if e := watchCurrentBinding(ctx, q, target.Binding); e != nil {
		return t, e
	}
	raw, e := q.GetDownloadWatchTaskMaterial(ctx, db.GetDownloadWatchTaskMaterialParams{TaskID: target.TaskID, ClientID: target.ClientID, RootID: target.Binding.RootID})
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return t, ports.ErrDownloadWatchStale
		}
		return t, watchError(ctx, e)
	}
	if e = statsDecode(raw, &t); e != nil {
		return t, ports.ErrUnavailable
	}
	current := watchTarget(t, target.Binding)
	if current != target || !t.Eligible || (tracking && t.State != "tracking") || !watchTaskBinding(t, target.Binding, ports.RootChangeHint{RelativePath: ".", Kind: ports.RootChangeRescan}) {
		return t, ports.ErrDownloadWatchStale
	}
	return t, nil
}
func ValidateDownloadWatchTargetAfterObservation(ctx context.Context, q *db.Queries, target ports.DownloadWatchTarget, observed db.DownloadTask) error {
	if e := watchGuard(ctx, q); e != nil {
		return e
	}
	if target.Kind != "ordinary" || observed.ID != target.TaskID || observed.OperationID != target.OperationID {
		return ports.ErrDownloadWatchStale
	}
	t, e := watchValidateMaterial(ctx, q, target, false)
	if e != nil {
		return e
	}
	if t.State != observed.State {
		return ports.ErrDownloadWatchStale
	}
	current, e := q.GetSubmissionTask(ctx, target.TaskID)
	if e != nil {
		return watchError(ctx, e)
	}
	// pgtype timestamps compare instants, independent of connection timezone.
	currentJSON, e := json.Marshal(current)
	if e != nil {
		return ports.ErrUnavailable
	}
	expectedJSON, e := json.Marshal(observed)
	if e != nil {
		return ports.ErrUnavailable
	}
	var a, b map[string]json.RawMessage
	if json.Unmarshal(currentJSON, &a) != nil || json.Unmarshal(expectedJSON, &b) != nil {
		return ports.ErrUnavailable
	}
	if len(a) != len(b) {
		return ports.ErrDownloadWatchStale
	}
	for key, v := range a {
		other, ok := b[key]
		if !ok {
			return ports.ErrDownloadWatchStale
		}
		if bytes.Equal(v, other) {
			continue
		}
		var at, bt time.Time
		if json.Unmarshal(v, &at) != nil || json.Unmarshal(other, &bt) != nil || !at.Equal(bt) {
			return ports.ErrDownloadWatchStale
		}
	}
	return ctx.Err()
}
