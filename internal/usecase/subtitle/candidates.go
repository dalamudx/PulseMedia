package subtitle

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"io/fs"
	"path"
)

type CandidateService struct {
	repo     ports.SubtitleCandidateRepository
	files    ports.MediaSubtitleFilesystem
	provider ports.SubtitleCandidateProvider
}

func NewCandidateService(repo ports.SubtitleCandidateRepository, files ports.MediaSubtitleFilesystem, provider ports.SubtitleCandidateProvider) *CandidateService {
	return &CandidateService{repo: repo, files: files, provider: provider}
}
func candidateAdmin(a *ports.Actor) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || a.Type != ports.ActorTypeUser || a.Scopes != nil || a.Role != ports.RoleAdmin {
		return ports.ErrForbidden
	}
	return nil
}
func (s *CandidateService) Read(ctx context.Context, a *ports.Actor, file uuid.UUID) (ports.SubtitleCandidateState, error) {
	if e := candidateAdmin(a); e != nil {
		return ports.SubtitleCandidateState{}, e
	}
	if file == uuid.Nil {
		return ports.SubtitleCandidateState{}, ports.ErrRejected
	}
	return s.repo.ReadCandidates(ctx, a, file)
}

// Reuse the original subtitle namespace/marker/inode checkpoints. This adapter
// grants validation only, and cannot complete or load an automatic subtitle job.
type candidateValidation struct {
	repo ports.SubtitleCandidateRepository
	t    ports.SubtitleCandidateTicket
}

func (v candidateValidation) LoadSubtitle(context.Context, ports.JobLease) (ports.MediaSidecarSnapshot, error) {
	return ports.MediaSidecarSnapshot{}, ports.ErrForbidden
}
func (v candidateValidation) CompleteSubtitle(context.Context, ports.JobLease, ports.MediaSidecarSnapshot, ports.MediaSubtitleCompletion) error {
	return ports.ErrForbidden
}
func (v candidateValidation) ValidateSubtitle(ctx context.Context, _ ports.JobLease, expected ports.MediaSidecarSnapshot) error {
	if expected.Binding != v.t.Snapshot.Binding {
		return ports.ErrMediaSidecarStale
	}
	return v.repo.ValidateCandidate(ctx, v.t)
}
func (s *CandidateService) safety(t ports.SubtitleCandidateTicket) *Service {
	return NewService(candidateValidation{s.repo, t}, s.files, nil)
}
func (s *CandidateService) Query(ctx context.Context, a *ports.Actor, file uuid.UUID, revision int64, key string) (out ports.SubtitleCandidateState, err error) {
	if e := candidateAdmin(a); e != nil {
		return out, e
	}
	t, out, e := s.repo.BeginCandidateQuery(ctx, a, file, revision, key)
	if e != nil || t.QueryID == uuid.Nil {
		return out, e
	}
	snapshot := t.Snapshot
	name, e := subtitleName(snapshot)
	if e != nil {
		return out, e
	}
	safe := s.safety(t)
	roots, release, e := s.files.LockRoots(ctx, snapshot.RootPath)
	if release != nil {
		defer release()
	}
	if e != nil {
		return out, e
	}
	root := roots[snapshot.RootPath]
	if root == nil || release == nil {
		return out, ports.ErrFileIdentity
	}
	directory := path.Dir(snapshot.FileRelative)
	dir, e := safe.observe(ctx, t.Lease, snapshot, root, directory, nil)
	if e != nil {
		return out, e
	}
	checkpoint := func() error { _, e := safe.observe(ctx, t.Lease, snapshot, root, directory, &dir); return e }
	finish := func(outcome string, c []ports.SubtitleCandidateContent) (ports.SubtitleCandidateState, error) {
		if e := checkpoint(); e != nil {
			return out, e
		}
		return s.repo.FinishCandidateQuery(ctx, t, outcome, c)
	}
	existing, inspect := root.Inspect(ctx, name)
	if e = checkpoint(); e != nil {
		return out, e
	}
	if inspect == nil {
		if !validFile(existing) || existing.Device != snapshot.RootIdentity.Device {
			return out, ports.ErrFileConflict
		}
		again, e := root.Inspect(ctx, name)
		if e != nil || again != existing {
			return out, ports.ErrFileIdentity
		}
		return finish("already_present", nil)
	}
	if !errors.Is(inspect, fs.ErrNotExist) {
		return out, ports.ErrFileConflict
	}
	if snapshot.FileIdentity.Size < ports.MinSubtitleVideoBytes {
		return finish("too_small", nil)
	}
	hash, e := s.files.HashSubtitleVideo(ctx, root, snapshot.FileRelative, snapshot.FileIdentity)
	if e != nil {
		return out, e
	}
	if e = checkpoint(); e != nil {
		return out, e
	}
	if hash.SizeBytes != snapshot.FileIdentity.Size || !media.ValidShooterHash(hash.Hash, hash.SizeBytes) {
		return out, ports.ErrMediaSidecarResult
	}
	if s.provider == nil {
		return out, ports.ErrUnavailable
	}
	candidates, fetchErr := s.provider.FindSubtitleCandidates(ctx, hash)
	if e = checkpoint(); e != nil {
		return out, e
	}
	if fetchErr != nil {
		return finish("provider_error", nil)
	}
	if len(candidates) > 8 {
		return finish("provider_error", nil)
	}
	if len(candidates) == 0 {
		return finish("not_found", nil)
	}
	unique := make([]ports.SubtitleCandidateContent, 0, len(candidates))
	seen := make(map[[sha256.Size]byte]struct{}, len(candidates))
	for _, c := range candidates {
		normalized, e := media.NormalizeChineseSRT(c.Content)
		if e != nil {
			return finish("provider_error", nil)
		}
		digest := sha256.Sum256(normalized)
		if _, exists := seen[digest]; exists {
			clear(normalized)
			continue
		}
		seen[digest] = struct{}{}
		unique = append(unique, ports.SubtitleCandidateContent{Content: normalized})
	}
	return finish("candidates", unique)
}
func (s *CandidateService) Confirm(ctx context.Context, a *ports.Actor, file, candidate uuid.UUID, revision int64) (out ports.SubtitleCandidateState, err error) {
	if e := candidateAdmin(a); e != nil {
		return out, e
	}
	if file == uuid.Nil || candidate == uuid.Nil || revision < 1 {
		return out, ports.ErrRejected
	}
	t, out, body, e := s.repo.BeginCandidateConfirmation(ctx, a, file, candidate, revision)
	if e != nil || t.QueryID == uuid.Nil {
		return out, e
	}
	defer clear(body)
	snapshot := t.Snapshot
	name, e := subtitleName(snapshot)
	if e != nil {
		return out, e
	}
	content, e := media.NormalizeChineseSRT(body)
	if e != nil {
		return out, ports.ErrMediaSidecarResult
	}
	safe := s.safety(t)
	roots, release, e := s.files.LockRoots(ctx, snapshot.RootPath)
	if release != nil {
		defer release()
	}
	if e != nil {
		return out, e
	}
	root := roots[snapshot.RootPath]
	if root == nil || release == nil {
		return out, ports.ErrFileIdentity
	}
	directory := path.Dir(snapshot.FileRelative)
	dir, e := safe.observe(ctx, t.Lease, snapshot, root, directory, nil)
	if e != nil {
		return out, e
	}
	checkpoint := func() error { _, e := safe.observe(ctx, t.Lease, snapshot, root, directory, &dir); return e }
	existing, inspect := root.Inspect(ctx, name)
	if e = checkpoint(); e != nil {
		return out, e
	}
	if inspect == nil {
		if !validFile(existing) || existing.Device != snapshot.RootIdentity.Device {
			return out, ports.ErrFileConflict
		}
		again, e := root.Inspect(ctx, name)
		if e != nil || again != existing {
			return out, ports.ErrFileIdentity
		}
		return s.repo.FinishCandidateConfirmation(ctx, t, ports.MediaSubtitleCompletion{Outcome: "observed", Name: name, Publication: ports.SidecarResult{Disposition: ports.SidecarPreservedRegular, Identity: existing}})
	}
	if !errors.Is(inspect, fs.ErrNotExist) {
		return out, ports.ErrFileConflict
	}
	hash, e := s.files.HashSubtitleVideo(ctx, root, snapshot.FileRelative, snapshot.FileIdentity)
	if e != nil {
		return out, e
	}
	if e = checkpoint(); e != nil {
		return out, e
	}
	if hash.SizeBytes != snapshot.FileIdentity.Size || !media.ValidShooterHash(hash.Hash, hash.SizeBytes) {
		return out, ports.ErrMediaSidecarResult
	}
	result, publishErr := s.files.PublishSubtitle(ctx, root, ports.SidecarRequest{SourceRelative: snapshot.FileRelative, SourceIdentity: snapshot.FileIdentity, DestinationDirectory: directory, DestinationIdentity: dir, Name: path.Base(name), Content: append([]byte(nil), content...)})
	if e = checkpoint(); e != nil {
		return out, errors.Join(publishErr, e)
	}
	completion := ports.MediaSubtitleCompletion{Outcome: "observed", VideoHash: hash, Name: name, Publication: result}
	out, e = s.repo.FinishCandidateConfirmation(ctx, t, completion)
	if e != nil {
		return out, e
	}
	if publishErr != nil && result.Disposition != ports.SidecarPreservedRegular && result.Disposition != ports.SidecarCreated {
		return out, publishErr
	}
	return out, nil
}
