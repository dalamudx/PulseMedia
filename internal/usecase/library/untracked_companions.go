package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type UntrackedCompanions struct {
	repo  ports.UntrackedCompanionRepository
	files ports.TransferFilesystem
}

func NewUntrackedCompanions(repo ports.UntrackedCompanionRepository, files ports.TransferFilesystem) *UntrackedCompanions {
	return &UntrackedCompanions{repo, files}
}
func companionDigest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func companionError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, safe := range []error{ports.ErrRejected, ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrNotFound, ports.ErrEndpointVersionConflict, ports.ErrUntrackedOrganizationStale, ports.ErrTransferExecutionRejected, ports.ErrTransferReviewRequired} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ports.ErrUnavailable
}
func (s *UntrackedCompanions) actor(ctx context.Context, a *ports.Actor, op uuid.UUID) (ports.Actor, error) {
	actor, err := untrackedScopeActor(ctx, a, op)
	if err != nil {
		return actor, err
	}
	if s == nil || s.repo == nil || s.files == nil {
		return actor, ports.ErrUnavailable
	}
	return actor, nil
}
func companionNamespace(ctx context.Context, files ports.RootFilesystem, scope ports.UntrackedScope, roots map[string]ports.TransferRoot) error {
	for _, item := range []struct {
		path   string
		id     ports.FileIdentity
		marker string
	}{{scope.LocalRoot, scope.SourceIdentity, ""}, {scope.TargetRootPath, scope.TargetIdentity, scope.TargetMarker}} {
		root := roots[item.path]
		if root == nil {
			return ports.ErrFileIdentity
		}
		id, err := root.Identity(ctx)
		if err != nil {
			return err
		}
		if untrackedScopePhysical(id) != untrackedScopePhysical(item.id) {
			return ports.ErrFileIdentity
		}
		fresh, err := files.OpenRoot(ctx, item.path)
		if err != nil {
			return err
		}
		observed, err := fresh.Identity(ctx)
		if err == nil && untrackedScopePhysical(observed) != untrackedScopePhysical(item.id) {
			err = ports.ErrFileIdentity
		}
		if err == nil && item.marker != "" {
			_, err = fresh.Inspect(ctx, item.marker)
		}
		fresh.Close()
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Strip only explicit subtitle tags, never arbitrary suffixes or episode numbers.
func companionStem(name string) (string, string) {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	base := stem
	for {
		i := strings.LastIndexByte(base, '.')
		if i < 0 {
			break
		}
		switch strings.ToLower(base[i+1:]) {
		case "zh", "zho", "chi", "chs", "cht", "zh-cn", "zh-tw", "zh-hans", "zh-hant", "sc", "tc", "cn", "en", "eng", "ja", "jpn", "jp", "ko", "kor", "fr", "fra", "fre", "de", "deu", "ger", "es", "spa", "ru", "rus", "pt", "por", "it", "ita", "ar", "ara", "und", "forced", "sdh", "cc":
			base = base[:i]
		default:
			return strings.ToLower(base), stem[len(base):] + strings.ToLower(ext)
		}
	}
	return strings.ToLower(base), stem[len(base):] + strings.ToLower(ext)
}
func matchCompanions(m ports.UntrackedOrganizationMaterial, entries []ports.UntrackedFileObservation) []ports.CompanionCandidate {
	source := m.Confirmation.Material.File.RelativePath
	target := m.Confirmation.Material.Input.TargetRelativePath
	stem := strings.ToLower(strings.TrimSuffix(path.Base(source), path.Ext(source)))
	videos := 0
	originalPresent := false
	for _, entry := range entries {
		if path.Dir(entry.RelativePath) != path.Dir(source) {
			continue
		}
		if entry.RelativePath == source {
			originalPresent = true
			if entry.Identity != m.Confirmation.Material.File.Identity {
				return []ports.CompanionCandidate{}
			}
		}
		if naming.IsVideoPath(entry.RelativePath) && strings.ToLower(strings.TrimSuffix(path.Base(entry.RelativePath), path.Ext(entry.RelativePath))) == stem {
			videos++
		}
	}
	// A moved original remains an explicit identity in the completed parent, not an invented video observation.
	if !originalPresent {
		videos++
	}
	out := []ports.CompanionCandidate{}
	if videos != 1 {
		return out
	}
	targets := map[string]int{}
	for _, entry := range entries {
		if path.Dir(entry.RelativePath) != path.Dir(source) || entry.Identity.Size < 1 {
			continue
		}
		ext := strings.ToLower(path.Ext(entry.RelativePath))
		if ext != ".srt" && ext != ".ass" {
			continue
		}
		base, suffix := companionStem(path.Base(entry.RelativePath))
		if base != stem {
			continue
		}
		dest := strings.TrimSuffix(target, path.Ext(target)) + suffix
		item := ports.CompanionCandidate{SourceRelativePath: entry.RelativePath, TargetRelativePath: dest, Identity: entry.Identity}
		item.Key = companionDigest([]any{m.Operation.ID, m.Operation.MaterialHash, item})
		out = append(out, item)
		targets[strings.ToLower(dest)]++
	}
	filtered := out[:0]
	for _, item := range out {
		if targets[strings.ToLower(item.TargetRelativePath)] == 1 {
			filtered = append(filtered, item)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].SourceRelativePath < filtered[j].SourceRelativePath })
	return filtered
}
func (s *UntrackedCompanions) discoverLocked(ctx context.Context, a *ports.Actor, op uuid.UUID, m ports.UntrackedOrganizationMaterial, roots map[string]ports.TransferRoot) ([]ports.CompanionCandidate, error) {
	dir, ok := s.files.(ports.CompanionDirectory)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	scope := m.Confirmation.Material.Scope
	if err := companionNamespace(ctx, s.files, scope, roots); err != nil {
		return nil, err
	}
	entries, err := dir.ListCompanionDirectory(ctx, roots[scope.LocalRoot], path.Dir(m.Confirmation.Material.File.RelativePath))
	if err != nil {
		return nil, err
	}
	if err = companionNamespace(ctx, s.files, scope, roots); err != nil {
		return nil, err
	}
	fresh, err := s.repo.CompanionMaterial(ctx, a, op)
	if err != nil {
		return nil, err
	}
	if companionDigest(fresh) != companionDigest(m) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	return matchCompanions(m, entries), nil
}
func (s *UntrackedCompanions) Discover(ctx context.Context, actor *ports.Actor, op uuid.UUID) (out []ports.CompanionCandidate, err error) {
	defer func() { err = companionError(ctx, err) }()
	a, err := s.actor(ctx, actor, op)
	if err != nil {
		return nil, err
	}
	m, err := s.repo.CompanionMaterial(ctx, &a, op)
	if err != nil {
		return nil, err
	}
	scope := m.Confirmation.Material.Scope
	roots, release, err := s.files.LockRoots(ctx, scope.LocalRoot, scope.TargetRootPath)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.discoverLocked(ctx, &a, op, m, roots)
}
func (s *UntrackedCompanions) Read(ctx context.Context, actor *ports.Actor, op uuid.UUID) (out *ports.CompanionView, err error) {
	defer func() { err = companionError(ctx, err) }()
	a, err := s.actor(ctx, actor, op)
	if err != nil {
		return nil, err
	}
	return s.repo.ReadCompanions(ctx, &a, op)
}
func (s *UntrackedCompanions) Select(ctx context.Context, actor *ports.Actor, op uuid.UUID, key string, in ports.UntrackedCompanionSelection) (out *ports.CompanionView, err error) {
	defer func() { err = companionError(ctx, err) }()
	a, err := s.actor(ctx, actor, op)
	if err != nil {
		return nil, err
	}
	if !untrackedScopeKey(key) || len(in.CandidateKeys) < 1 || len(in.CandidateKeys) > 32 || (in.Action != "copy" && in.Action != "hardlink") {
		return nil, ports.ErrRejected
	}
	keys := append([]string{}, in.CandidateKeys...)
	sort.Strings(keys)
	for i, k := range keys {
		if !untrackedReviewHash(k) || (i > 0 && k == keys[i-1]) {
			return nil, ports.ErrRejected
		}
	}
	hash := companionDigest([]any{keys, in.Action})
	old, err := s.repo.ReplayCompanionSelection(ctx, &a, op, key, hash)
	if err != nil || old != nil {
		return old, err
	}
	m, err := s.repo.CompanionMaterial(ctx, &a, op)
	if err != nil {
		return nil, err
	}
	scope := m.Confirmation.Material.Scope
	roots, release, err := s.files.LockRoots(ctx, scope.LocalRoot, scope.TargetRootPath)
	if err != nil {
		return nil, err
	}
	defer release()
	candidates, err := s.discoverLocked(ctx, &a, op, m, roots)
	if err != nil {
		return nil, err
	}
	selected := []ports.CompanionCandidate{}
	for _, key := range keys {
		for _, item := range candidates {
			if item.Key == key {
				selected = append(selected, item)
			}
		}
	}
	if len(selected) != len(keys) {
		return nil, ports.ErrUntrackedOrganizationStale
	}
	for _, item := range selected {
		id, e := roots[scope.LocalRoot].Inspect(ctx, item.SourceRelativePath)
		if e != nil {
			return nil, e
		}
		if id != item.Identity {
			return nil, ports.ErrFileIdentity
		}
		if _, e = roots[scope.TargetRootPath].Inspect(ctx, item.TargetRelativePath); !errors.Is(e, os.ErrNotExist) {
			if e == nil {
				return nil, ports.ErrTransferReviewRequired
			}
			return nil, e
		}
	}
	return s.repo.SelectCompanions(ctx, &a, op, key, hash, selected, in.Action)
}
func (s *UntrackedCompanions) Execute(ctx context.Context, actor *ports.Actor, op, id uuid.UUID) (out *ports.CompanionAttachment, err error) {
	defer func() { err = companionError(ctx, err) }()
	a, err := s.actor(ctx, actor, op)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, ports.ErrRejected
	}
	view, err := s.repo.ReadCompanions(ctx, &a, op)
	if err != nil {
		return nil, err
	}
	var item *ports.CompanionAttachment
	for _, v := range view.Items {
		if v.ID == id {
			copy := v
			item = &copy
			break
		}
	}
	if item == nil {
		return nil, ports.ErrNotFound
	}
	if item.State == "completed" {
		return item, nil
	}
	cap, ok := s.files.(ports.VerifiedTransferFilesystem)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	m, err := s.repo.CompanionMaterial(ctx, &a, op)
	if err != nil {
		return nil, err
	}
	scope := m.Confirmation.Material.Scope
	roots, release, err := s.files.LockRoots(ctx, scope.LocalRoot, scope.TargetRootPath)
	if err != nil {
		return nil, err
	}
	defer release()
	if err = companionNamespace(ctx, s.files, scope, roots); err != nil {
		return nil, err
	}
	e, err := s.repo.ClaimCompanion(ctx, &a, op, id)
	if err != nil {
		return nil, err
	}
	// No goroutine: even a native call that ignores cancellation owns both physical slots until return.
	defer func() {
		if err != nil {
			finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = s.repo.FinishCompanion(finish, &a, *e, nil)
		}
	}()
	source, target := roots[scope.LocalRoot], roots[scope.TargetRootPath]
	candidate := e.Attachment.Candidate
	if e.Content == nil {
		// An interrupted claim without a content permit proves no completion; it never grants a new write opportunity.
		if e.Token != 1 {
			return nil, ports.ErrTransferReviewRequired
		}
		// Freeze a permanent permit only after current content, target absence and authorization checks.
		if _, err = target.Inspect(ctx, candidate.TargetRelativePath); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				err = ports.ErrTransferReviewRequired
			}
			return nil, err
		}
		proof, failure := cap.InspectContent(ctx, source, candidate.SourceRelativePath, candidate.Identity)
		if failure != nil {
			return nil, failure
		}
		if err = companionNamespace(ctx, s.files, scope, roots); err != nil {
			return nil, err
		}
		if err = s.repo.SealCompanion(ctx, &a, *e, proof); err != nil {
			return nil, err
		}
		e.Content = &proof
		if _, err = s.repo.CompanionMaterial(ctx, &a, op); err != nil {
			return nil, err
		}
		if err = companionNamespace(ctx, s.files, scope, roots); err != nil {
			return nil, err
		}
		if err = s.repo.ValidateCompanion(ctx, &a, *e); err != nil {
			return nil, err
		}
		if e.Attachment.Action == "hardlink" {
			err = s.files.Hardlink(ctx, source, candidate.SourceRelativePath, target, candidate.TargetRelativePath, proof.File)
		} else {
			_, err = cap.TransferVerified(ctx, source, candidate.SourceRelativePath, target, candidate.TargetRelativePath, proof, "copy", id)
		}
		if err != nil {
			return nil, err
		}
	}
	// Existing permits can only enter read-only observation; no copy/link is retried.
	var result ports.VerifiedTransferResult
	sourceProof, err := cap.InspectContent(ctx, source, candidate.SourceRelativePath, candidate.Identity)
	if err != nil {
		return nil, err
	}
	if sourceProof != *e.Content {
		return nil, ports.ErrTransferReviewRequired
	}
	if e.Attachment.Action == "hardlink" {
		targetIdentity, failure := target.Inspect(ctx, candidate.TargetRelativePath)
		if failure != nil {
			return nil, failure
		}
		if targetIdentity != candidate.Identity {
			return nil, ports.ErrTransferReviewRequired
		}
		result.Target, err = cap.InspectContent(ctx, target, candidate.TargetRelativePath, targetIdentity)
	} else {
		result, err = cap.ObserveTransfer(ctx, source, candidate.SourceRelativePath, target, candidate.TargetRelativePath, *e.Content, "copy", id)
	}
	if err != nil {
		return nil, err
	}
	if result.SourceRemoved || result.Target.SHA256 != e.Content.SHA256 || result.Target.File.Size != e.Content.File.Size {
		return nil, ports.ErrTransferReviewRequired
	}
	if err = companionNamespace(ctx, s.files, scope, roots); err != nil {
		return nil, err
	}
	if err = s.repo.FinishCompanion(ctx, &a, *e, &result); err != nil {
		return nil, err
	}
	e.Attachment.State = "completed"
	return &e.Attachment, nil
}
