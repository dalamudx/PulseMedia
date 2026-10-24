package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const untrackedScopeTTL = 15 * time.Second

type untrackedScopeCapture struct {
	store                 *UntrackedScopeStore
	actor                 ports.Actor
	input                 ports.UntrackedScopeInput
	key, hash             string
	material              ports.UntrackedScopeMaterial
	previous              *ports.UntrackedScope
	retained              bool
	deadline, issuedLocal time.Time
}

func (c *untrackedScopeCapture) UntrackedScopeMaterial() ports.UntrackedScopeMaterial {
	m := c.material
	if m.TargetRoot.RootIdentity != nil {
		identity := *m.TargetRoot.RootIdentity
		m.TargetRoot.RootIdentity = &identity
	}
	return m
}

func untrackedError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrRestoreIsolated) {
		return err
	}
	return statsError(err)
}

func untrackedValid(in ports.UntrackedScopeInput) bool {
	return in.ExpectedVersion >= 0 && in.ExpectedVersion < math.MaxInt64 && in.ClientID != uuid.Nil && !in.ExpectedClientVersion.IsZero() && in.ExpectedRootGeneration > 0 && in.TargetRootID != uuid.Nil && watchPath(in.RemotePrefix)
}

func untrackedHash(in ports.UntrackedScopeInput) string {
	in.ExpectedClientVersion = in.ExpectedClientVersion.UTC()
	return watchDigest(in)
}

func untrackedIdentity(i ports.FileIdentity) ports.FileIdentity {
	return ports.FileIdentity{Device: i.Device, Inode: i.Inode}
}

func untrackedSame(a, b ports.UntrackedScope) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) || !a.UpdatedAt.Equal(b.UpdatedAt) || !a.ClientVersion.Equal(b.ClientVersion) {
		return false
	}
	a.CreatedAt, a.UpdatedAt, a.ClientVersion = b.CreatedAt, b.UpdatedAt, b.ClientVersion
	return a == b
}

func untrackedScopeValid(c ports.UntrackedScope) bool {
	stamp, e := hex.DecodeString(c.ClientStamp)
	return e == nil && len(stamp) == 32 && strings.ToLower(c.ClientStamp) == c.ClientStamp && c.ID != uuid.Nil && c.OwnerID != uuid.Nil && c.Version > 0 && c.ClientID != uuid.Nil && !c.ClientVersion.IsZero() && watchPath(c.RemotePrefix) && watchPath(c.LocalRoot) && c.LocalRoot != "/" && c.TargetRootID != uuid.Nil && watchPath(c.TargetRootPath) && c.TargetRootPath != "/" && c.TargetRootGeneration > 0 && (c.TargetMediaType == "movie" || c.TargetMediaType == "tv") && c.TargetBaselineScanID != uuid.Nil && c.TargetIdentity.Device >= 0 && c.TargetIdentity.Inode > 0 && c.TargetIdentity == untrackedIdentity(c.TargetIdentity) && c.SourceIdentity == untrackedIdentity(c.SourceIdentity) && (!c.Enabled || (c.SourceIdentity.Device >= 0 && c.SourceIdentity.Inode > 0)) && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt)
}

type untrackedCurrentRow struct {
	ID           uuid.UUID       `json:"id"`
	OwnerID      uuid.UUID       `json:"owner_id"`
	Enabled      bool            `json:"enabled"`
	Version      int64           `json:"version"`
	ClientID     uuid.UUID       `json:"client_id"`
	TargetRootID uuid.UUID       `json:"target_root_id"`
	LocalRoot    string          `json:"local_root"`
	Snapshot     json.RawMessage `json:"snapshot"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type untrackedReceiptRow struct {
	ScopeID     uuid.UUID       `json:"scope_id"`
	OwnerID     uuid.UUID       `json:"owner_id"`
	CommandKey  string          `json:"command_key"`
	Version     int64           `json:"version"`
	Enabled     bool            `json:"enabled"`
	LocalRoot   string          `json:"local_root"`
	RequestHash string          `json:"request_hash"`
	Input       json.RawMessage `json:"input"`
	Snapshot    json.RawMessage `json:"snapshot"`
}

func untrackedVerified(row untrackedReceiptRow) (*ports.UntrackedScopeReceipt, error) {
	var in ports.UntrackedScopeInput
	var c ports.UntrackedScope
	if checkinDecode(row.Input, &in) != nil || checkinDecode(row.Snapshot, &c) != nil || !untrackedValid(in) || !untrackedScopeValid(c) || !doubanSyncASCII(row.CommandKey, 160, false) || row.RequestHash != untrackedHash(in) || row.ScopeID != c.ID || row.OwnerID != c.OwnerID || row.Version != c.Version || row.Enabled != c.Enabled || row.LocalRoot != c.LocalRoot || c.Version != in.ExpectedVersion+1 || c.Enabled != in.Enabled || c.ClientID != in.ClientID || !c.ClientVersion.Equal(in.ExpectedClientVersion) || c.RemotePrefix != in.RemotePrefix || c.TargetRootID != in.TargetRootID || c.TargetRootGeneration != in.ExpectedRootGeneration {
		return nil, ports.ErrEndpointVersionConflict
	}
	return &ports.UntrackedScopeReceipt{CommandKey: row.CommandKey, Scope: c, ExecutionAllowed: false}, nil
}

func untrackedCurrent(ctx context.Context, tx db.DBTX, owner, id uuid.UUID, lock bool) (*ports.UntrackedScope, error) {
	q := db.New(tx)
	var raw []byte
	var err error
	if lock {
		raw, err = q.LockUntrackedScopeRow(ctx, id)
	} else {
		raw, err = q.GetUntrackedScopeRow(ctx, id)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var row untrackedCurrentRow
	if checkinDecode(raw, &row) != nil {
		return nil, ports.ErrEndpointVersionConflict
	}
	if row.OwnerID != owner {
		return nil, ports.ErrForbidden
	}
	var c ports.UntrackedScope
	if checkinDecode(row.Snapshot, &c) != nil || !untrackedScopeValid(c) || c.ID != row.ID || c.OwnerID != row.OwnerID || c.Enabled != row.Enabled || c.Version != row.Version || c.ClientID != row.ClientID || c.TargetRootID != row.TargetRootID || c.LocalRoot != row.LocalRoot || !c.CreatedAt.Equal(row.CreatedAt) || !c.UpdatedAt.Equal(row.UpdatedAt) {
		return nil, ports.ErrEndpointVersionConflict
	}
	receiptRaw, err := q.GetUntrackedScopeReceiptVersionRow(ctx, db.GetUntrackedScopeReceiptVersionRowParams{ScopeID: id, Version: c.Version})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ports.ErrEndpointVersionConflict
		}
		return nil, err
	}
	var receipt untrackedReceiptRow
	if checkinDecode(receiptRaw, &receipt) != nil {
		return nil, ports.ErrEndpointVersionConflict
	}
	r, err := untrackedVerified(receipt)
	if err != nil {
		return nil, err
	}
	if !untrackedSame(r.Scope, c) {
		return nil, ports.ErrEndpointVersionConflict
	}
	return &r.Scope, nil
}

func untrackedReceipt(ctx context.Context, tx db.DBTX, owner, id uuid.UUID, key string, current *ports.UntrackedScope) (*ports.UntrackedScopeReceipt, string, error) {
	raw, err := db.New(tx).GetUntrackedScopeReceiptRow(ctx, db.GetUntrackedScopeReceiptRowParams{ScopeID: id, CommandKey: key})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ports.ErrNotFound
		}
		return nil, "", err
	}
	var row untrackedReceiptRow
	if checkinDecode(raw, &row) != nil {
		return nil, "", ports.ErrEndpointVersionConflict
	}
	if row.OwnerID != owner {
		return nil, "", ports.ErrForbidden
	}
	r, err := untrackedVerified(row)
	if err != nil {
		return nil, "", err
	}
	if current == nil || row.ScopeID != id || row.CommandKey != key || r.Scope.Version > current.Version || !r.Scope.CreatedAt.Equal(current.CreatedAt) || r.Scope.UpdatedAt.After(current.UpdatedAt) {
		return nil, "", ports.ErrEndpointVersionConflict
	}
	return r, row.RequestHash, nil
}

func untrackedRetain(in ports.UntrackedScopeInput, old *ports.UntrackedScope) bool {
	return !in.Enabled && old != nil && in.ClientID == old.ClientID && in.TargetRootID == old.TargetRootID && in.RemotePrefix == old.RemotePrefix && in.ExpectedClientVersion.Equal(old.ClientVersion) && in.ExpectedRootGeneration == old.TargetRootGeneration
}

func untrackedOldMaterial(c ports.UntrackedScope) ports.UntrackedScopeMaterial {
	identity := c.TargetIdentity
	return ports.UntrackedScopeMaterial{ScopeID: c.ID, OwnerID: c.OwnerID, ExpectedVersion: c.Version, Enabled: false, SourceIdentity: c.SourceIdentity, ClientID: c.ClientID, ClientVersion: c.ClientVersion, ClientStamp: c.ClientStamp, RemotePrefix: c.RemotePrefix, LocalRoot: c.LocalRoot, TargetRoot: ports.ScanRootSnapshot{ID: c.TargetRootID, Path: c.TargetRootPath, MediaType: c.TargetMediaType, MarkerFilename: c.TargetMarker, Generation: c.TargetRootGeneration, Health: "online", RootIdentity: &identity}, TargetBaselineScanID: c.TargetBaselineScanID, TargetIdentity: identity}
}

func untrackedMaterial(ctx context.Context, q *db.Queries, owner, id uuid.UUID, in ports.UntrackedScopeInput) (ports.UntrackedScopeMaterial, error) {
	var m ports.UntrackedScopeMaterial
	raw, err := q.GetDownloadWatchRootMaterial(ctx, db.GetDownloadWatchRootMaterialParams{ClientID: in.ClientID, RootID: in.TargetRootID})
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ports.ErrNotFound
	}
	if err != nil {
		return m, err
	}
	var r watchRootMaterial
	if statsDecode(raw, &r) != nil {
		return m, ports.ErrRejected
	}
	// Validate the entire map, including entries unrelated to this selection.
	mapping, err := watchMapping(r.Mapping)
	if err != nil {
		return m, ports.ErrRejected
	}
	local, ok := mapping[in.RemotePrefix]
	if !ok || len(mapping) == 0 {
		return m, ports.ErrRejected
	}
	longest := ""
	for prefix := range mapping {
		if watchContained(prefix, in.RemotePrefix) && len(prefix) > len(longest) {
			longest = prefix
		}
	}
	if longest != in.RemotePrefix {
		return m, ports.ErrRejected
	}
	if !r.ClientVersion.Equal(in.ExpectedClientVersion) || r.Generation != in.ExpectedRootGeneration {
		return m, ports.ErrEndpointVersionConflict
	}
	if _, reason := watchSources(r); reason != "" || r.Device < 0 || r.Inode <= 0 {
		return m, ports.ErrRejected
	}
	identity := ports.FileIdentity{Device: r.Device, Inode: r.Inode}
	m = ports.UntrackedScopeMaterial{ScopeID: id, OwnerID: owner, ExpectedVersion: in.ExpectedVersion, Enabled: in.Enabled, ClientID: r.ClientID, ClientVersion: r.ClientVersion.UTC(), ClientStamp: r.ClientStamp, RemotePrefix: in.RemotePrefix, LocalRoot: local, TargetRoot: ports.ScanRootSnapshot{ID: r.RootID, Path: r.RootPath, MediaType: r.MediaType, MarkerFilename: r.Marker, Generation: r.Generation, Health: "online", RootIdentity: &identity}, TargetBaselineScanID: r.BaselineID, TargetIdentity: identity}
	return m, nil
}

func untrackedLockSelection(ctx context.Context, tx db.DBTX, m ports.UntrackedScopeMaterial) error {
	q := db.New(tx)
	if _, err := q.LockUntrackedScopeClient(ctx, m.ClientID); err != nil {
		return err
	}
	_, err := q.LockUntrackedScopeRoots(ctx, db.LockUntrackedScopeRootsParams{TargetRootID: m.TargetRoot.ID, SourcePath: m.LocalRoot})
	return err
}

// The caller must hold its existing library-root lock in this same transaction,
// and call again after deferred effects before committing an automatic link.
// A historic enabled source remains a review boundary after disable/revocation.
func GuardUntrackedMovieRecognition(ctx context.Context, q *db.Queries, rootID uuid.UUID, relPath string) error {
	if q == nil {
		return ports.ErrUnavailable
	}
	if rootID == uuid.Nil || relPath == "" || relPath == "." || path.IsAbs(relPath) || path.Clean(relPath) != relPath || relPath == ".." || strings.HasPrefix(relPath, "../") || strings.Contains(relPath, "\\") || !watchPath("/"+relPath) {
		return ports.ErrMovieLinkStale
	}
	exists, err := q.UntrackedScopeHistoryExists(ctx)
	if err != nil {
		return untrackedError(ctx, err)
	}
	if !exists {
		return nil
	}
	root, err := q.UntrackedScopeRecognitionRoot(ctx, rootID)
	if err != nil {
		return untrackedError(ctx, err)
	}
	if !watchPath(root) {
		return ports.ErrMovieLinkStale
	}
	blocked, err := q.UntrackedScopeRecognitionBlocked(ctx, path.Join(root, relPath))
	if err != nil {
		return untrackedError(ctx, err)
	}
	if blocked {
		return ports.ErrMovieLinkStale
	}
	return guardExternalTransferTarget(ctx, q, rootID, relPath)
}
