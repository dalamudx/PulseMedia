package library

import (
	"context"
	"errors"
	"math"
	"path"
	"path/filepath"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type UntrackedScopeService struct {
	repo  ports.UntrackedScopeRepository
	files ports.RootFilesystem
}

func NewUntrackedScopeService(repo ports.UntrackedScopeRepository, files ports.RootFilesystem) *UntrackedScopeService {
	return &UntrackedScopeService{repo: repo, files: files}
}

func untrackedScopeActor(ctx context.Context, actor *ports.Actor, id uuid.UUID) (ports.Actor, error) {
	if err := ctx.Err(); err != nil {
		return ports.Actor{}, err
	}
	if actor == nil {
		return ports.Actor{}, ports.ErrUnauthorized
	}
	a := *actor
	if actor.Scopes != nil {
		a.Scopes = append([]string{}, actor.Scopes...)
	}
	owner, err := uuid.Parse(a.ID)
	if err != nil || owner == uuid.Nil || owner.String() != a.ID || a.Type != ports.ActorTypeUser || a.Role != ports.RoleAdmin || a.Scopes != nil {
		return ports.Actor{}, ports.ErrForbidden
	}
	if id == uuid.Nil {
		return ports.Actor{}, ports.ErrRejected
	}
	return a, nil
}

func untrackedScopeKey(key string) bool {
	if len(key) == 0 || len(key) > 160 {
		return false
	}
	for _, c := range []byte(key) {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != ':' && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func untrackedScopeInput(in ports.UntrackedScopeInput) bool {
	return in.ExpectedVersion >= 0 && in.ExpectedVersion < math.MaxInt64 && in.ClientID != uuid.Nil && !in.ExpectedClientVersion.IsZero() && in.TargetRootID != uuid.Nil && in.ExpectedRootGeneration >= 1 && path.IsAbs(in.RemotePrefix) && path.Clean(in.RemotePrefix) == in.RemotePrefix && !strings.ContainsAny(in.RemotePrefix, "\x00\\")
}

func untrackedScopePath(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsRune(p, 0)
}

func untrackedScopeIdentity(i ports.FileIdentity) bool {
	return i.Device >= 0 && i.Inode > 0
}

func untrackedScopePhysical(i ports.FileIdentity) ports.FileIdentity {
	return ports.FileIdentity{Device: i.Device, Inode: i.Inode}
}

// Return only public sentinels, never filesystem paths or repository error text.
func untrackedScopeError(err error) error {
	for _, safe := range []error{ports.ErrRejected, ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrNotFound, ports.ErrEndpointVersionConflict} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}

func untrackedScopeMaterialValid(m ports.UntrackedScopeMaterial, id uuid.UUID, owner string, in ports.UntrackedScopeInput) bool {
	r := m.TargetRoot
	marker := r.MarkerFilename
	return m.ScopeID == id && m.OwnerID != uuid.Nil && m.OwnerID.String() == owner && m.ExpectedVersion == in.ExpectedVersion && m.Enabled == in.Enabled && m.ClientID == in.ClientID && m.ClientVersion.Equal(in.ExpectedClientVersion) && m.ClientStamp != "" && m.RemotePrefix == in.RemotePrefix && untrackedScopePath(m.LocalRoot) && r.ID == in.TargetRootID && r.Generation == in.ExpectedRootGeneration && untrackedScopePath(r.Path) && (r.MediaType == "movie" || r.MediaType == "tv") && marker != "" && marker != "." && marker != ".." && !strings.ContainsAny(marker, "/\\\x00") && m.TargetBaselineScanID != uuid.Nil && untrackedScopeIdentity(m.TargetIdentity)
}

func untrackedScopeValid(c ports.UntrackedScope, id uuid.UUID) bool {
	in := ports.UntrackedScopeInput{Enabled: c.Enabled, ExpectedVersion: c.Version - 1, ClientID: c.ClientID, ExpectedClientVersion: c.ClientVersion, RemotePrefix: c.RemotePrefix, TargetRootID: c.TargetRootID, ExpectedRootGeneration: c.TargetRootGeneration}
	m := ports.UntrackedScopeMaterial{ScopeID: c.ID, OwnerID: c.OwnerID, ExpectedVersion: in.ExpectedVersion, Enabled: c.Enabled, ClientID: c.ClientID, ClientVersion: c.ClientVersion, ClientStamp: c.ClientStamp, RemotePrefix: c.RemotePrefix, LocalRoot: c.LocalRoot, TargetRoot: ports.ScanRootSnapshot{ID: c.TargetRootID, Path: c.TargetRootPath, Generation: c.TargetRootGeneration, MediaType: c.TargetMediaType, MarkerFilename: c.TargetMarker}, TargetBaselineScanID: c.TargetBaselineScanID, TargetIdentity: c.TargetIdentity}
	return c.Version > 0 && untrackedScopeInput(in) && untrackedScopeMaterialValid(m, id, c.OwnerID.String(), in) && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt) && (!c.Enabled || untrackedScopeIdentity(c.SourceIdentity))
}

func untrackedScopeReceiptValid(r *ports.UntrackedScopeReceipt, id uuid.UUID, owner, key string) bool {
	return r != nil && !r.ExecutionAllowed && r.CommandKey == key && r.Scope.OwnerID.String() == owner && untrackedScopeValid(r.Scope, id)
}

func untrackedScopeMatches(c ports.UntrackedScope, in ports.UntrackedScopeInput) bool {
	return c.Version == in.ExpectedVersion+1 && c.Enabled == in.Enabled && c.ClientID == in.ClientID && c.ClientVersion.Equal(in.ExpectedClientVersion) && c.RemotePrefix == in.RemotePrefix && c.TargetRootID == in.TargetRootID && c.TargetRootGeneration == in.ExpectedRootGeneration
}

func (s *UntrackedScopeService) Read(ctx context.Context, actor *ports.Actor, id uuid.UUID) (*ports.UntrackedScope, error) {
	a, err := untrackedScopeActor(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	c, err := s.repo.ReadUntrackedScope(ctx, &a, id)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, untrackedScopeError(err)
	}
	if c == nil {
		return nil, nil
	}
	if !untrackedScopeValid(*c, id) || c.OwnerID.String() != a.ID {
		return nil, ports.ErrUnavailable
	}
	out := *c
	return &out, nil
}

func (s *UntrackedScopeService) Receipt(ctx context.Context, actor *ports.Actor, id uuid.UUID, key string) (*ports.UntrackedScopeReceipt, error) {
	a, err := untrackedScopeActor(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if !untrackedScopeKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	r, err := s.repo.ReadUntrackedScopeReceipt(ctx, &a, id, key)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, untrackedScopeError(err)
	}
	if !untrackedScopeReceiptValid(r, id, actor.ID, key) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}

func (s *UntrackedScopeService) Save(ctx context.Context, actor *ports.Actor, id uuid.UUID, in ports.UntrackedScopeInput, key string) (*ports.UntrackedScopeReceipt, error) {
	a, err := untrackedScopeActor(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if !untrackedScopeInput(in) || !untrackedScopeKey(key) {
		return nil, ports.ErrRejected
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	owner := a.ID
	p, err := s.repo.PrepareUntrackedScope(ctx, &a, id, in, key)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, untrackedScopeError(err)
	}
	if p == nil || (p.Receipt == nil) == (p.Capture == nil) {
		return nil, ports.ErrUnavailable
	}
	if p.Receipt != nil {
		if !untrackedScopeReceiptValid(p.Receipt, id, owner, key) || !untrackedScopeMatches(p.Receipt.Scope, in) {
			return nil, ports.ErrUnavailable
		}
		out := *p.Receipt
		return &out, nil
	}
	m := p.Capture.UntrackedScopeMaterial()
	if !untrackedScopeMaterialValid(m, id, owner, in) {
		return nil, ports.ErrUnavailable
	}
	var evidence ports.UntrackedScopeEvidence
	if in.Enabled {
		if s.files == nil {
			return nil, ports.ErrUnavailable
		}
		evidence, err = probeUntrackedScope(ctx, s.files, m)
		if err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	r, err := s.repo.CommitUntrackedScope(ctx, p.Capture, evidence)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, untrackedScopeError(err)
	}
	if !untrackedScopeReceiptValid(r, id, owner, key) || !untrackedScopeMatches(r.Scope, in) {
		return nil, ports.ErrUnavailable
	}
	expectedSource := evidence.SourceIdentity
	if !in.Enabled {
		expectedSource = untrackedScopePhysical(m.SourceIdentity)
	}
	c := r.Scope
	if c.ClientStamp != m.ClientStamp || c.LocalRoot != m.LocalRoot || c.TargetRootPath != m.TargetRoot.Path || c.TargetMediaType != m.TargetRoot.MediaType || c.TargetMarker != m.TargetRoot.MarkerFilename || c.TargetBaselineScanID != m.TargetBaselineScanID || c.SourceIdentity != expectedSource || c.TargetIdentity != untrackedScopePhysical(m.TargetIdentity) {
		return nil, ports.ErrUnavailable
	}
	out := *r
	return &out, nil
}

// Every capability stays owned until the actual synchronous operation returns.
func untrackedRootProbe(ctx context.Context, files ports.RootFilesystem, rootPath, marker string) (ports.FileIdentity, ports.FileIdentity, error) {
	var zero ports.FileIdentity
	if err := ctx.Err(); err != nil {
		return zero, zero, err
	}
	root, err := files.OpenRoot(ctx, rootPath)
	if root != nil {
		defer root.Close()
	}
	if ctx.Err() != nil {
		return zero, zero, ctx.Err()
	}
	if err != nil || root == nil {
		return zero, zero, ports.ErrUnavailable
	}
	identity, err := root.Identity(ctx)
	if ctx.Err() != nil {
		return zero, zero, ctx.Err()
	}
	if err != nil || !untrackedScopeIdentity(identity) {
		return zero, zero, ports.ErrUnavailable
	}
	var markerIdentity ports.FileIdentity
	if marker != "" {
		markerIdentity, err = root.Inspect(ctx, marker)
		if ctx.Err() != nil {
			return zero, zero, ctx.Err()
		}
		if err != nil || !untrackedScopeIdentity(markerIdentity) {
			return zero, zero, ports.ErrUnavailable
		}
	}
	return untrackedScopePhysical(identity), markerIdentity, nil
}

func probeUntrackedScope(ctx context.Context, files ports.RootFilesystem, m ports.UntrackedScopeMaterial) (ports.UntrackedScopeEvidence, error) {
	var evidence ports.UntrackedScopeEvidence
	source, _, err := untrackedRootProbe(ctx, files, m.LocalRoot, "")
	if err != nil {
		return evidence, err
	}
	target, marker, err := untrackedRootProbe(ctx, files, m.TargetRoot.Path, m.TargetRoot.MarkerFilename)
	if err != nil {
		return evidence, err
	}
	if target != untrackedScopePhysical(m.TargetIdentity) {
		return evidence, ports.ErrUnavailable
	}
	currentSource, _, err := untrackedRootProbe(ctx, files, m.LocalRoot, "")
	if err != nil {
		return evidence, err
	}
	if currentSource != source {
		return evidence, ports.ErrUnavailable
	}
	currentTarget, currentMarker, err := untrackedRootProbe(ctx, files, m.TargetRoot.Path, m.TargetRoot.MarkerFilename)
	if err != nil {
		return evidence, err
	}
	if currentTarget != target || currentMarker != marker {
		return evidence, ports.ErrUnavailable
	}
	return ports.UntrackedScopeEvidence{SourceIdentity: source, TargetIdentity: target}, nil
}
