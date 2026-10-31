package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LibraryMaintenanceStore struct {
	pool  *pgxpool.Pool
	files ports.LibraryMaintenanceFilesystem
	ids   ports.IDGen
}

func NewLibraryMaintenanceStore(pool *pgxpool.Pool, files ports.LibraryMaintenanceFilesystem, ids ports.IDGen) (*LibraryMaintenanceStore, error) {
	if pool == nil || files == nil || ids == nil {
		return nil, ports.ErrUnavailable
	}
	return &LibraryMaintenanceStore{pool, files, ids}, nil
}

var _ ports.LibraryMaintenanceRepository = (*LibraryMaintenanceStore)(nil)

type maintenanceSnapshot struct {
	File struct {
		ID          uuid.UUID  `json:"id"`
		RootID      uuid.UUID  `json:"root_id"`
		Rel         string     `json:"rel_path"`
		Size        int64      `json:"size_bytes"`
		Mtime       int64      `json:"mtime_ns"`
		Fingerprint string     `json:"file_fingerprint"`
		Retired     *time.Time `json:"retired_at"`
	} `json:"file"`
	Root struct {
		ID         uuid.UUID `json:"id"`
		Path       string    `json:"path"`
		Generation int64     `json:"generation"`
		Health     string    `json:"health"`
		Baseline   bool      `json:"baseline_completed"`
		Marker     string    `json:"marker_filename"`
	} `json:"root"`
	Baseline *struct {
		Device int64 `json:"device"`
		Inode  int64 `json:"inode"`
	} `json:"baseline"`
	Mappings []struct {
		UnitID  uuid.UUID  `json:"unit_id"`
		Retired *time.Time `json:"retired_at"`
	} `json:"mappings"`
	Facts []struct {
		ID      uuid.UUID       `json:"id"`
		UnitID  uuid.UUID       `json:"unit_id"`
		RootID  uuid.UUID       `json:"root_id"`
		Stage   string          `json:"stage"`
		Profile json.RawMessage `json:"quality_profile"`
		Revoked *time.Time      `json:"revoked_at"`
		Expires *time.Time      `json:"expires_at"`
	} `json:"facts"`
	Units []struct {
		ID      uuid.UUID `json:"id"`
		MediaID uuid.UUID `json:"media_id"`
		Key     string    `json:"unit_key"`
		Season  *int64    `json:"season_number"`
		Episode *int64    `json:"episode_number"`
		Retired bool      `json:"retired"`
	} `json:"units"`
	Media []struct {
		ID   uuid.UUID `json:"id"`
		Type string    `json:"media_type"`
	} `json:"media"`
	Lanes        []json.RawMessage `json:"lanes"`
	Reservations []json.RawMessage `json:"reservations"`
	Transfers    []json.RawMessage `json:"transfers"`
}
type maintenanceRecycle struct {
	ID            string             `json:"recycle_id"`
	Kind          string             `json:"kind"`
	OldFileID     uuid.UUID          `json:"old_file_id"`
	ReplacementID uuid.UUID          `json:"replacement_file_id"`
	RootID        uuid.UUID          `json:"root_id"`
	Original      string             `json:"original_rel"`
	Recycle       string             `json:"recycle_rel"`
	Retained      time.Time          `json:"retained_until"`
	Identity      ports.FileIdentity `json:"identity"`
	RootIdentity  ports.FileIdentity `json:"root_identity"`
	RootPath      string             `json:"root_path"`
	Generation    *int64             `json:"root_generation"`
}
type maintenanceFrozen struct {
	SourceState      json.RawMessage    `json:"source_state"`
	ReplacementState json.RawMessage    `json:"replacement_state,omitempty"`
	ReplacementID    *uuid.UUID         `json:"replacement_id,omitempty"`
	Retirement       json.RawMessage    `json:"retirement,omitempty"`
	Source           string             `json:"source"`
	Target           string             `json:"target"`
	RootPath         string             `json:"root_path"`
	Root             ports.FileIdentity `json:"root"`
	Marker           ports.FileIdentity `json:"marker"`
	Identity         ports.FileIdentity `json:"identity"`
	Replacement      ports.FileIdentity `json:"replacement"`
}

func maintenanceHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func maintenanceChanged() error { return ports.ErrLibraryMaintenanceChanged }
func maintenancePath(p string) bool {
	if len(p) < 1 || len(p) > 1024 || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00\r\n") {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." || strings.HasPrefix(s, ".pulsemedia") {
			return false
		}
	}
	return path.Ext(p) == ".mkv" || path.Ext(p) == ".mp4"
}
func maintenanceKey(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if r < 33 || r > 126 || r == ',' {
			return false
		}
	}
	return true
}
func maintenanceActor(a *ports.Actor) (uuid.UUID, error) {
	if a == nil {
		return uuid.Nil, ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || !a.IsAdmin() || a.Scopes != nil {
		return uuid.Nil, ports.ErrForbidden
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil {
		return uuid.Nil, ports.ErrForbidden
	}
	return id, nil
}
func maintenanceError(e error) error {
	if e == nil {
		return nil
	}
	for _, known := range []error{ports.ErrUnauthorized, ports.ErrForbidden, ports.ErrNotFound, ports.ErrLibraryMaintenanceChanged, ports.ErrRejected, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(e, known) {
			return known
		}
	}
	return ports.ErrUnavailable
}
func maintenanceUser(ctx context.Context, tx pgx.Tx, a *ports.Actor) (uuid.UUID, error) {
	id, e := maintenanceActor(a)
	if e != nil {
		return uuid.Nil, e
	}
	row, e := db.New(tx).LockLibraryMaintenanceUser(ctx, id)
	active, role := row.IsActive, row.Role
	if errors.Is(e, pgx.ErrNoRows) || e == nil && (!active || role != "admin") {
		return uuid.Nil, ports.ErrForbidden
	}
	return id, e
}
func maintenanceState(ctx context.Context, tx pgx.Tx, id uuid.UUID) (json.RawMessage, maintenanceSnapshot, error) {
	var s maintenanceSnapshot
	raw, e := db.New(tx).GetLibraryMaintenanceSnapshot(ctx, id)
	if e != nil {
		return nil, s, e
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, s, ports.ErrNotFound
	}
	if json.Unmarshal(raw, &s) != nil {
		return nil, s, maintenanceChanged()
	}
	return raw, s, nil
}
func maintenanceRootValid(s maintenanceSnapshot) bool {
	return s.Root.Health == "online" && s.Root.Baseline && s.Root.Generation > 0 && s.Baseline != nil && s.Baseline.Inode > 0 && len(s.Lanes) == 0 && len(s.Reservations) == 0 && len(s.Transfers) == 0
}
func maintenanceCanonical(s maintenanceSnapshot) bool {
	if len(s.Mappings) != 1 || len(s.Units) != 1 || len(s.Media) != 1 || s.Units[0].Retired || s.Mappings[0].UnitID != s.Units[0].ID || s.Units[0].MediaID != s.Media[0].ID {
		return false
	}
	if s.Media[0].Type == "movie" {
		return s.Units[0].Key == "movie"
	}
	return s.Media[0].Type == "tv" && s.Units[0].Season != nil && s.Units[0].Episode != nil && *s.Units[0].Season > 0 && *s.Units[0].Episode > 0 && s.Units[0].Key == fmt.Sprintf("S%02dE%02d", *s.Units[0].Season, *s.Units[0].Episode)
}
func maintenanceKnownQuality(raw json.RawMessage) bool {
	var p quality.Profile
	return json.Unmarshal(raw, &p) == nil && quality.KnownProfile(p) && p.ResolutionParserVersion == quality.ResolutionTokenParserVersion
}
func maintenanceImported(s maintenanceSnapshot, at time.Time) bool {
	count := 0
	for _, f := range s.Facts {
		if f.Revoked == nil && (f.Expires == nil || f.Expires.After(at)) {
			if (f.Stage != "imported" && f.Stage != "confirmed") || f.UnitID != s.Units[0].ID || f.RootID != s.Root.ID || !maintenanceKnownQuality(f.Profile) {
				return false
			}
			count++
		}
	}
	return count > 0
}
func maintenanceLock(ctx context.Context, tx pgx.Tx, s maintenanceSnapshot, other uuid.UUID) error {
	q := db.New(tx)
	if e := q.LockLibraryMaintenanceRoot(ctx, s.Root.ID); e != nil {
		return e
	}
	if e := q.LockLibraryMaintenanceUnits(ctx, s.File.ID); e != nil {
		return e
	}
	if e := q.LockLibraryMaintenanceMedia(ctx, s.File.ID); e != nil {
		return e
	}
	if e := q.LockLibraryMaintenanceFiles(ctx, db.LockLibraryMaintenanceFilesParams{FileID: s.File.ID, OtherID: other}); e != nil {
		return e
	}
	if e := q.LockLibraryMaintenanceMappings(ctx, db.LockLibraryMaintenanceMappingsParams{FileID: s.File.ID, OtherID: other}); e != nil {
		return e
	}
	if e := q.LockLibraryMaintenanceFacts(ctx, db.LockLibraryMaintenanceFactsParams{FileID: s.File.ID, OtherID: other}); e != nil {
		return e
	}
	return nil
}
func maintenanceRecycleState(ctx context.Context, tx pgx.Tx, id string) (json.RawMessage, maintenanceRecycle, error) {
	var r maintenanceRecycle
	raw, e := db.New(tx).GetLibraryVerifiedRecycle(ctx, id)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, r, ports.ErrNotFound
	}
	if e != nil {
		return nil, r, e
	}
	if json.Unmarshal(raw, &r) != nil || r.Identity.Inode == 0 || r.RootIdentity.Inode == 0 || r.Retained.IsZero() {
		return nil, r, maintenanceChanged()
	}
	return raw, r, nil
}
func maintenanceRecycleStatus(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	row, e := db.New(tx).GetLibraryMaintenanceRecycleStatus(ctx, id)
	action, status := row.Action, row.Status
	if errors.Is(e, pgx.ErrNoRows) {
		return "retained", nil
	}
	if e != nil {
		return "", e
	}
	if status != "completed" {
		return "unknown_requires_review", nil
	}
	if action == "restore" {
		return "restored", nil
	}
	return "cleaned", nil
}
func maintenanceDBTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	at, e := db.New(tx).GetLibraryMaintenanceClock(ctx)
	return at.Time, e
}

// material is a current, narrow transaction. It does not open a filesystem or
// retain any transaction across a physical call.
func (s *LibraryMaintenanceStore) material(ctx context.Context, tx pgx.Tx, a *ports.Actor, c ports.LibraryMaintenanceCommand, lock bool) (maintenanceFrozen, maintenanceSnapshot, error) {
	var f maintenanceFrozen
	var state maintenanceSnapshot
	if _, e := maintenanceUser(ctx, tx, a); e != nil {
		return f, state, e
	}
	fid := c.FileID
	var rec maintenanceRecycle
	var e error
	if c.Action != "reorganize" {
		f.Retirement, rec, e = maintenanceRecycleState(ctx, tx, c.RecycleID)
		if e != nil {
			return f, state, e
		}
		fid = rec.OldFileID
		f.ReplacementID = &rec.ReplacementID
	}
	f.SourceState, state, e = maintenanceState(ctx, tx, fid)
	if e != nil {
		return f, state, e
	}
	if lock {
		other := uuid.Nil
		if f.ReplacementID != nil {
			other = *f.ReplacementID
		}
		if e = maintenanceLock(ctx, tx, state, other); e != nil {
			return f, state, e
		}
		f.SourceState, state, e = maintenanceState(ctx, tx, fid)
		if e != nil {
			return f, state, e
		}
	}
	at, e := maintenanceDBTime(ctx, tx)
	if e != nil {
		return f, state, e
	}
	if !maintenanceRootValid(state) || !maintenanceCanonical(state) {
		return f, state, maintenanceChanged()
	}
	f.RootPath = state.Root.Path
	if c.Action == "reorganize" {
		if !maintenancePath(state.File.Rel) || !maintenancePath(c.TargetRelativePath) || state.File.Rel == c.TargetRelativePath || state.File.Retired != nil || state.Mappings[0].Retired != nil || !maintenanceImported(state, at) {
			return f, state, maintenanceChanged()
		}
		f.Source = state.File.Rel
		f.Target = c.TargetRelativePath
	} else {
		status, e := maintenanceRecycleStatus(ctx, tx, c.RecycleID)
		if e != nil {
			return f, state, e
		}
		if status != "retained" || state.File.Retired == nil || state.File.Rel != rec.Original || state.Root.ID != rec.RootID || rec.RootPath != state.Root.Path || rec.Generation != nil && *rec.Generation != state.Root.Generation || !maintenancePath(rec.Original) || !strings.HasPrefix(rec.Recycle, ".pulsemedia_recycle/") {
			return f, state, maintenanceChanged()
		}
		for _, fact := range state.Facts {
			if fact.Revoked == nil {
				return f, state, maintenanceChanged()
			}
		}
		f.ReplacementState, _, e = maintenanceState(ctx, tx, rec.ReplacementID)
		if e != nil {
			return f, state, e
		}
		var replacement maintenanceSnapshot
		if json.Unmarshal(f.ReplacementState, &replacement) != nil || replacement.File.Retired != nil || replacement.Root.ID != state.Root.ID || !maintenanceCanonical(replacement) || !maintenanceImported(replacement, at) || replacement.Units[0].ID != state.Units[0].ID {
			return f, state, maintenanceChanged()
		}
		if c.Action == "cleanup" && at.Before(rec.Retained) {
			return f, state, maintenanceChanged()
		}
		f.Root = rec.RootIdentity
		f.Identity = rec.Identity
		f.Source = rec.Recycle
		if c.Action == "restore" {
			f.Target = rec.Original
		}
	}
	if f.Target != "" {
		var exists bool
		if exists, e = db.New(tx).LibraryMaintenanceTargetExists(ctx, db.LibraryMaintenanceTargetExistsParams{RootID: state.Root.ID, Target: f.Target}); e != nil {
			return f, state, e
		}
		if exists {
			return f, state, maintenanceChanged()
		}
	}
	return f, state, nil
}
func (s *LibraryMaintenanceStore) inspect(ctx context.Context, root ports.TransferRoot, f *maintenanceFrozen, state maintenanceSnapshot, moved bool) (ports.MaintenanceFileObservation, error) {
	var empty ports.MaintenanceFileObservation
	if e := s.maintenanceConfiguredRoot(ctx, *f, state); e != nil {
		return empty, e
	}
	id, e := root.Identity(ctx)
	if e != nil || id.Device != state.Baseline.Device || id.Inode != state.Baseline.Inode {
		return empty, maintenanceChanged()
	}
	if f.Root.Inode != 0 && (f.Root.Device != id.Device || f.Root.Inode != id.Inode) {
		return empty, maintenanceChanged()
	}
	f.Root = id
	marker := state.Root.Marker
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	m, e := root.Inspect(ctx, marker)
	if e != nil || m.Size < 1 {
		return empty, maintenanceChanged()
	}
	if f.Marker.Inode != 0 && f.Marker != m {
		return empty, maintenanceChanged()
	}
	f.Marker = m
	source := f.Source
	if moved && f.Target != "" {
		source = f.Target
	}
	o, e := s.files.MaintenanceInspect(ctx, root, source)
	if e != nil {
		return empty, e
	}
	if f.Identity.Inode != 0 && f.Identity != o.Identity {
		return empty, maintenanceChanged()
	}
	if state.File.Size != o.Identity.Size || state.File.Mtime != o.Identity.Mtime || state.File.Fingerprint != fmt.Sprintf("stat.v1:%d:%d:%d", o.Identity.Inode, o.Identity.Size, o.Identity.Mtime) {
		return empty, maintenanceChanged()
	}
	f.Identity = o.Identity
	if f.ReplacementID != nil {
		var repl maintenanceSnapshot
		if json.Unmarshal(f.ReplacementState, &repl) != nil {
			return empty, maintenanceChanged()
		}
		ri, e := root.Inspect(ctx, repl.File.Rel)
		if e != nil || ri.Size != repl.File.Size || ri.Mtime != repl.File.Mtime || repl.File.Fingerprint != fmt.Sprintf("stat.v1:%d:%d:%d", ri.Inode, ri.Size, ri.Mtime) || ri.Device == o.Identity.Device && ri.Inode == o.Identity.Inode {
			return empty, maintenanceChanged()
		}
		if f.Replacement.Inode != 0 && f.Replacement != ri {
			return empty, maintenanceChanged()
		}
		f.Replacement = ri
	}
	absent := f.Target
	if moved {
		absent = f.Source
	}
	if absent != "" {
		if _, e = root.Inspect(ctx, absent); !errors.Is(e, fs.ErrNotExist) {
			return empty, maintenanceChanged()
		}
	}
	return o, nil
}
func maintenanceSameMaterial(a, b maintenanceFrozen) bool {
	return preparationSameJSON(a.SourceState, b.SourceState) && preparationSameJSON(a.ReplacementState, b.ReplacementState) && preparationSameJSON(a.Retirement, b.Retirement) && a.Source == b.Source && a.Target == b.Target && a.RootPath == b.RootPath
}

func (s *LibraryMaintenanceStore) PreviewReorganization(ctx context.Context, a *ports.Actor, r ports.LibraryReorganizationRequest) (out *ports.LibraryReorganizationPreview, err error) {
	defer func() { err = maintenanceError(err) }()
	if r.FileID == uuid.Nil || !maintenancePath(r.TargetRelativePath) {
		return nil, ports.ErrRejected
	}
	c := ports.LibraryMaintenanceCommand{Action: "reorganize", FileID: r.FileID, TargetRelativePath: r.TargetRelativePath}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	f, state, e := s.material(ctx, tx, a, c, false)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	roots, release, e := s.files.LockRoots(ctx, f.RootPath)
	if e != nil {
		return nil, e
	}
	defer release()
	root := roots[f.RootPath]
	if root == nil {
		return nil, ports.ErrUnavailable
	}
	if _, e = s.inspect(ctx, root, &f, state, false); e != nil {
		return nil, e
	}
	// Freeze the physical observation only if the database still has that exact graph.
	tx, e = s.pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer upgradeRollback(ctx, tx)
	next, _, e := s.material(ctx, tx, a, c, true)
	if e != nil {
		return nil, e
	}
	if !maintenanceSameMaterial(f, next) {
		return nil, maintenanceChanged()
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return &ports.LibraryReorganizationPreview{FileID: r.FileID, RootID: state.Root.ID, SourceRelativePath: f.Source, TargetRelativePath: f.Target, Version: maintenanceHash([]any{"library.maintenance.v1", c.Action, f}), CanConfirm: true}, nil
}
