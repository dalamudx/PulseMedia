package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type untrackedDiscoveryPayload struct {
	ScopeID      uuid.UUID `json:"scope_id"`
	ScopeVersion int64     `json:"scope_version"`
	Sequence     int64     `json:"sequence"`
}

type untrackedDiscoveryState struct {
	ScopeID               uuid.UUID  `json:"scope_id"`
	RequestedVersion      int64      `json:"requested_version"`
	RequestedSequence     int64      `json:"requested_sequence"`
	CompletedSequence     int64      `json:"completed_sequence"`
	ActiveJobID           *uuid.UUID `json:"active_job_id"`
	ActiveSequence        int64      `json:"active_sequence"`
	ScheduledScopeVersion int64      `json:"scheduled_scope_version"`
	NextSweepAt           time.Time  `json:"next_sweep_at"`
}

type untrackedDiscoveryCapture struct {
	store    *UntrackedDiscoveryStore
	snapshot ports.UntrackedDiscoverySnapshot
}

func (c *untrackedDiscoveryCapture) UntrackedDiscoverySnapshot() ports.UntrackedDiscoverySnapshot {
	return c.snapshot
}

func discoveryError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	for _, stale := range []error{ports.ErrUntrackedDiscoveryStale, ports.ErrEndpointVersionConflict, pgx.ErrNoRows} {
		if errors.Is(err, stale) {
			return ports.ErrUntrackedDiscoveryStale
		}
	}
	return untrackedError(ctx, err)
}

func discoverySkippable(err error) bool {
	return errors.Is(err, ports.ErrUntrackedDiscoveryStale) || errors.Is(err, ports.ErrForbidden) || errors.Is(err, ports.ErrNotFound) || errors.Is(err, ports.ErrRejected)
}

func discoveryLimit(n int) (int, error) {
	if n == 0 {
		return 64, nil
	}
	if n < 1 || n > 64 {
		return 0, ports.ErrRejected
	}
	return n, nil
}

func discoveryObservationValid(o ports.UntrackedFileObservation) bool {
	p, i := o.RelativePath, o.Identity
	if !utf8.ValidString(p) || p == "" || p == "." || p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) || path.Clean(p) != p || !watchPath("/"+p) || i.Device < 0 || i.Inode <= 0 || i.Size < 0 {
		return false
	}
	return naming.IsVideoPath(p)
}

func discoveryPayload(raw []byte) (untrackedDiscoveryPayload, error) {
	var p untrackedDiscoveryPayload
	var fields map[string]json.RawMessage
	if checkinDecode(raw, &p) != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 3 || p.ScopeID == uuid.Nil || p.ScopeVersion <= 0 || p.Sequence <= 0 {
		return p, ports.ErrUntrackedDiscoveryStale
	}
	return p, nil
}

func discoveryState(raw []byte) (untrackedDiscoveryState, error) {
	var d untrackedDiscoveryState
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 8 {
		return d, ports.ErrUntrackedDiscoveryStale
	}
	for _, key := range []string{"scope_id", "requested_version", "requested_sequence", "completed_sequence", "active_job_id", "active_sequence", "scheduled_scope_version", "next_sweep_at"} {
		value, exists := fields[key]
		if !exists || (key != "active_job_id" && string(value) == "null") {
			return d, ports.ErrUntrackedDiscoveryStale
		}
	}
	if statsDecode(raw, &d) != nil || d.ScopeID == uuid.Nil || d.RequestedVersion <= 0 || d.RequestedSequence < 0 || d.CompletedSequence < 0 || d.CompletedSequence > d.RequestedSequence || d.ActiveSequence < 0 || d.ActiveSequence > d.RequestedSequence || d.NextSweepAt.IsZero() || (d.ActiveJobID != nil && (*d.ActiveJobID == uuid.Nil || d.ActiveSequence <= 0 || d.ScheduledScopeVersion <= 0)) {
		return d, ports.ErrUntrackedDiscoveryStale
	}
	return d, nil
}

func discoveryInput(c ports.UntrackedScope) ports.UntrackedScopeInput {
	return ports.UntrackedScopeInput{Enabled: true, ExpectedVersion: c.Version, ClientID: c.ClientID, ExpectedClientVersion: c.ClientVersion, RemotePrefix: c.RemotePrefix, TargetRootID: c.TargetRootID, ExpectedRootGeneration: c.TargetRootGeneration}
}

// Source identity is the explicit scope receipt, never a matching library root.
func discoveryMaterial(ctx context.Context, q *db.Queries, c ports.UntrackedScope) (ports.UntrackedScopeMaterial, error) {
	m, err := untrackedMaterial(ctx, q, c.OwnerID, c.ID, discoveryInput(c))
	if err != nil {
		return m, err
	}
	if !c.Enabled || m.ClientStamp != c.ClientStamp || !m.ClientVersion.Equal(c.ClientVersion) || m.LocalRoot != c.LocalRoot || m.TargetRoot.Path != c.TargetRootPath || m.TargetRoot.MediaType != c.TargetMediaType || m.TargetRoot.MarkerFilename != c.TargetMarker || m.TargetRoot.Generation != c.TargetRootGeneration || m.TargetBaselineScanID != c.TargetBaselineScanID || m.TargetIdentity != c.TargetIdentity || c.SourceIdentity.Device < 0 || c.SourceIdentity.Inode <= 0 {
		return m, ports.ErrUntrackedDiscoveryStale
	}
	m.SourceIdentity = c.SourceIdentity
	return m, nil
}

func discoveryActor(owner uuid.UUID) *ports.Actor {
	return &ports.Actor{Type: ports.ActorTypeUser, ID: owner.String(), Role: ports.RoleAdmin}
}

func (s *UntrackedDiscoveryStore) ready() bool {
	return s != nil && s.pool != nil && s.ids != nil && s.clock != nil && s.cursorKey != uuid.Nil
}

// Called before scope/state/job locks in all writing paths.
func (s *UntrackedDiscoveryStore) authority(ctx context.Context, tx pgx.Tx, id uuid.UUID, owner uuid.UUID, lock bool) (*ports.UntrackedScope, error) {
	q := db.New(tx)
	if lock {
		if err := untrackedGuard(ctx, q, discoveryActor(owner)); err != nil {
			return nil, err
		}
	} else {
		if err := discoveryReadGuard(ctx, tx); err != nil {
			return nil, err
		}
		allowed, err := q.UntrackedDiscoveryReadAdmin(ctx, owner)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, ports.ErrForbidden
		}
	}
	c, err := untrackedCurrent(ctx, tx, owner, id, false)
	if err != nil {
		return nil, err
	}
	if c == nil || !c.Enabled {
		return nil, ports.ErrUntrackedDiscoveryStale
	}
	m, err := discoveryMaterial(ctx, q, *c)
	if err != nil {
		return nil, err
	}
	if lock {
		if err = untrackedLockSelection(ctx, tx, m); err != nil {
			return nil, err
		}
		locked, e := untrackedCurrent(ctx, tx, owner, id, true)
		if e != nil {
			return nil, e
		}
		if locked == nil || !untrackedSame(*c, *locked) {
			return nil, ports.ErrUntrackedDiscoveryStale
		}
		if _, err = discoveryMaterial(ctx, q, *locked); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (s *UntrackedDiscoveryStore) recheck(ctx context.Context, tx pgx.Tx, c ports.UntrackedScope) error {
	current, err := s.authority(ctx, tx, c.ID, c.OwnerID, false)
	if err != nil {
		return err
	}
	if !untrackedSame(c, *current) {
		return ports.ErrUntrackedDiscoveryStale
	}
	return nil
}

func discoveryTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

type untrackedDiscoveryCursor struct {
	Kind         string    `json:"kind"`
	Owner        uuid.UUID `json:"owner"`
	Scope        uuid.UUID `json:"scope"`
	Version      int64     `json:"version"`
	Horizon      time.Time `json:"horizon"`
	AfterCreated time.Time `json:"after_created"`
	AfterID      uuid.UUID `json:"after_id"`
}

// A cursor cannot refresh its horizon or transfer to another owner/scope/version.
// Keys are instance-local; restarting the store requires restarting pagination.
func (s *UntrackedDiscoveryStore) encodeCursor(c untrackedDiscoveryCursor) string {
	raw, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *UntrackedDiscoveryStore) decodeCursor(raw, kind string, owner, scope uuid.UUID, version int64) (untrackedDiscoveryCursor, error) {
	var c untrackedDiscoveryCursor
	if len(raw) > 2048 {
		return c, ports.ErrRejected
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return c, ports.ErrRejected
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return c, ports.ErrRejected
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return c, ports.ErrRejected
	}
	mac := hmac.New(sha256.New, s.cursorKey[:])
	_, _ = mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) || checkinDecode(b, &c) != nil || c.Kind != kind || c.Owner != owner || c.Scope != scope || c.Version != version || c.Horizon.IsZero() || c.AfterID == uuid.Nil || c.AfterCreated.After(c.Horizon) || s.encodeCursor(c) != raw {
		return c, ports.ErrRejected
	}
	return c, nil
}

type untrackedDiscoveredRow struct {
	ID              uuid.UUID `json:"id"`
	ScopeID         uuid.UUID `json:"scope_id"`
	ScopeVersion    int64     `json:"scope_version"`
	RelativePath    string    `json:"relative_path"`
	Revision        int64     `json:"revision"`
	Device          int64     `json:"device"`
	Inode           int64     `json:"inode"`
	Size            int64     `json:"size"`
	Mtime           int64     `json:"mtime"`
	CreatedAt       time.Time `json:"created_at"`
	FirstObservedAt time.Time `json:"first_observed_at"`
	LastObservedAt  time.Time `json:"last_observed_at"`
	Observations    int64     `json:"observations"`
	LastJobID       uuid.UUID `json:"last_job_id"`
	LastToken       int64     `json:"last_token"`
}

func discoveryFile(raw []byte, scope uuid.UUID, version int64) (untrackedDiscoveredRow, ports.UntrackedDiscoveredFile, error) {
	var r untrackedDiscoveredRow
	if checkinDecode(raw, &r) != nil {
		return r, ports.UntrackedDiscoveredFile{}, ports.ErrUnavailable
	}
	f := ports.UntrackedDiscoveredFile{ID: r.ID, ScopeID: r.ScopeID, ScopeVersion: r.ScopeVersion, Revision: r.Revision, RelativePath: r.RelativePath, Identity: ports.FileIdentity{Device: r.Device, Inode: r.Inode, Size: r.Size, Mtime: r.Mtime}, FirstObservedAt: r.FirstObservedAt, LastObservedAt: r.LastObservedAt, Observations: r.Observations}
	if r.ID == uuid.Nil || r.ScopeID != scope || r.ScopeVersion < 1 || r.ScopeVersion > version || r.Revision < 1 || r.Observations < 1 || r.LastJobID == uuid.Nil || r.LastToken < 1 || r.CreatedAt.IsZero() || r.FirstObservedAt.IsZero() || r.LastObservedAt.Before(r.FirstObservedAt) || !discoveryObservationValid(ports.UntrackedFileObservation{RelativePath: f.RelativePath, Identity: f.Identity}) {
		return r, ports.UntrackedDiscoveredFile{}, ports.ErrUnavailable
	}
	return r, f, nil
}
