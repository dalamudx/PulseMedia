package subtitle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type Service struct {
	repo     ports.MediaSubtitleRepository
	files    ports.MediaSubtitleFilesystem
	provider ports.ChineseSubtitleProvider
}

func NewService(repo ports.MediaSubtitleRepository, files ports.MediaSubtitleFilesystem, provider ports.ChineseSubtitleProvider) *Service {
	return &Service{repo: repo, files: files, provider: provider}
}

func (s *Service) Execute(ctx context.Context, lease ports.JobLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, err := s.repo.LoadSubtitle(ctx, lease)
	if err != nil {
		return errors.Join(err, ctx.Err())
	}
	snapshot = cloneSnapshot(snapshot)
	name, err := subtitleName(snapshot)
	if err != nil {
		return err
	}
	if err := s.validate(ctx, lease, snapshot); err != nil {
		return err
	}
	roots, release, err := s.files.LockRoots(ctx, snapshot.RootPath)
	if release != nil {
		defer release()
	}
	if err := s.after(ctx, lease, snapshot, err); err != nil {
		return err
	}
	root := roots[snapshot.RootPath]
	if root == nil || release == nil {
		return ports.ErrFileIdentity
	}
	directory := path.Dir(snapshot.FileRelative)
	dirID, err := s.observe(ctx, lease, snapshot, root, directory, nil)
	if err != nil {
		return err
	}
	checkpoint := func() error {
		_, err := s.observe(ctx, lease, snapshot, root, directory, &dirID)
		return err
	}
	complete := func(c ports.MediaSubtitleCompletion) error {
		if err := checkpoint(); err != nil {
			return err
		}
		if err := s.validate(ctx, lease, snapshot); err != nil {
			return err
		}
		err := s.repo.CompleteSubtitle(ctx, lease, cloneSnapshot(snapshot), c)
		return errors.Join(err, ctx.Err())
	}
	existing, inspectErr := root.Inspect(ctx, name)
	if err := s.after(ctx, lease, snapshot, nil); err != nil {
		return err
	}
	if err := checkpoint(); err != nil {
		return err
	}
	if inspectErr == nil {
		if !validFile(existing) || existing.Device != snapshot.RootIdentity.Device {
			return ports.ErrFileConflict
		}
		// Reobserve the actual entry; preserving a regular file asserts no ownership.
		current, err := root.Inspect(ctx, name)
		if err := s.after(ctx, lease, snapshot, err); err != nil {
			return err
		}
		if current != existing {
			return ports.ErrFileIdentity
		}
		return complete(ports.MediaSubtitleCompletion{Outcome: "observed", Name: name, Publication: ports.SidecarResult{Disposition: ports.SidecarPreservedRegular, Identity: existing}})
	}
	if !errors.Is(inspectErr, fs.ErrNotExist) {
		return errors.Join(ports.ErrFileConflict, inspectErr)
	}
	if snapshot.FileIdentity.Size < ports.MinSubtitleVideoBytes {
		return complete(ports.MediaSubtitleCompletion{Outcome: "too_small"})
	}
	if err := checkpoint(); err != nil {
		return err
	}
	hash, hashErr := s.files.HashSubtitleVideo(ctx, root, snapshot.FileRelative, snapshot.FileIdentity)
	if err := s.after(ctx, lease, snapshot, hashErr); err != nil {
		return err
	}
	if err := checkpoint(); err != nil {
		return err
	}
	if hash.SizeBytes != snapshot.FileIdentity.Size || !media.ValidShooterHash(hash.Hash, hash.SizeBytes) {
		return ports.ErrMediaSidecarResult
	}
	if s.provider == nil {
		return ports.ErrMediaSidecarUnavailable
	}
	body, fetchErr := s.provider.FindChineseSubtitle(ctx, hash)
	noResult := body == nil
	body = append([]byte(nil), body...)
	if err := s.after(ctx, lease, snapshot, fetchErr); err != nil {
		return err
	}
	if err := checkpoint(); err != nil {
		return err
	}
	if noResult {
		return complete(ports.MediaSubtitleCompletion{Outcome: "not_found", VideoHash: hash})
	}
	content, err := media.NormalizeChineseSRT(body)
	if err != nil {
		return err
	}
	if err := checkpoint(); err != nil {
		return err
	}
	result, publishErr := s.files.PublishSubtitle(ctx, root, ports.SidecarRequest{
		SourceRelative: snapshot.FileRelative, SourceIdentity: snapshot.FileIdentity,
		DestinationDirectory: directory, DestinationIdentity: dirID, Name: path.Base(name), Content: append([]byte(nil), content...),
	})
	// The synchronous call owns the physical slot even after cancellation.
	if err := s.after(ctx, lease, snapshot, nil); err != nil {
		return errors.Join(publishErr, err)
	}
	if err := checkpoint(); err != nil {
		return errors.Join(publishErr, err)
	}
	outcomeErr := publicationError(result, snapshot.RootIdentity.Device)
	if publishErr != nil && outcomeErr == nil {
		return publishErr
	}
	completion := ports.MediaSubtitleCompletion{Outcome: "observed", VideoHash: hash, Name: name, Publication: result}
	return errors.Join(publishErr, outcomeErr, complete(completion))
}

func (s *Service) validate(ctx context.Context, lease ports.JobLease, snapshot ports.MediaSidecarSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.repo.ValidateSubtitle(ctx, lease, cloneSnapshot(snapshot))
	return errors.Join(err, ctx.Err())
}

func (s *Service) after(ctx context.Context, lease ports.JobLease, snapshot ports.MediaSidecarSnapshot, callErr error) error {
	return errors.Join(callErr, s.validate(ctx, lease, snapshot))
}

// Reopen the configured namespace: a pinned capability alone cannot detect replacement.
func (s *Service) observe(ctx context.Context, lease ports.JobLease, snapshot ports.MediaSidecarSnapshot, root ports.TransferRoot, directory string, expected *ports.SidecarDirectoryIdentity) (ports.SidecarDirectoryIdentity, error) {
	var zero ports.SidecarDirectoryIdentity
	if err := s.validate(ctx, lease, snapshot); err != nil {
		return zero, err
	}
	id, err := root.Identity(ctx)
	if err := s.after(ctx, lease, snapshot, err); err != nil {
		return zero, err
	}
	if !sameDirectory(id, snapshot.RootIdentity) {
		return zero, ports.ErrFileIdentity
	}
	namespace, openErr := s.files.OpenRoot(ctx, snapshot.RootPath)
	if namespace != nil {
		defer namespace.Close()
	}
	if err := s.after(ctx, lease, snapshot, openErr); err != nil {
		return zero, err
	}
	if namespace == nil {
		return zero, ports.ErrFileIdentity
	}
	id, err = namespace.Identity(ctx)
	if err := s.after(ctx, lease, snapshot, err); err != nil {
		return zero, err
	}
	if !sameDirectory(id, snapshot.RootIdentity) {
		return zero, ports.ErrFileIdentity
	}
	marker := snapshot.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	_, err = namespace.Inspect(ctx, marker)
	if err := s.after(ctx, lease, snapshot, err); err != nil {
		return zero, err
	}
	for _, capability := range []ports.TransferRoot{root, namespace} {
		id, err = capability.Inspect(ctx, snapshot.FileRelative)
		if err := s.after(ctx, lease, snapshot, err); err != nil {
			return zero, err
		}
		if id != snapshot.FileIdentity {
			return zero, ports.ErrFileIdentity
		}
	}
	dirID, err := s.files.InspectSidecarDirectory(ctx, root, directory)
	if err := s.after(ctx, lease, snapshot, err); err != nil {
		return zero, err
	}
	if dirID.Device != snapshot.RootIdentity.Device || dirID.Inode <= 0 || (expected != nil && dirID != *expected) {
		return zero, ports.ErrFileIdentity
	}
	namespaceDir, err := s.files.InspectSidecarDirectory(ctx, namespace, directory)
	if err := s.after(ctx, lease, snapshot, err); err != nil {
		return zero, err
	}
	if namespaceDir != dirID {
		return zero, ports.ErrFileIdentity
	}
	return dirID, nil
}

func publicationError(r ports.SidecarResult, device int64) error {
	if validFile(r.Identity) && r.Identity.Device == device && ((r.Disposition == ports.SidecarCreated && r.Published) || (r.Disposition == ports.SidecarPreservedRegular && !r.Published)) {
		return nil
	}
	if r.Disposition == ports.SidecarUnsafeExisting {
		return ports.ErrFileConflict
	}
	return ports.ErrMediaSidecarResult
}

func validFile(id ports.FileIdentity) bool { return id.Device > 0 && id.Inode > 0 && id.Size >= 0 }
func sameDirectory(a, b ports.FileIdentity) bool {
	return b.Device > 0 && b.Inode > 0 && a.Device == b.Device && a.Inode == b.Inode
}

func subtitleName(s ports.MediaSidecarSnapshot) (string, error) {
	bad := ports.ErrMediaSidecarUnavailable
	if s.Binding == "" || strings.TrimSpace(s.Binding) != s.Binding || strings.ContainsAny(s.Binding, "\x00\r\n") || s.Source.Version != 1 || s.Source.SourceEventID == uuid.Nil || s.Source.FileID == uuid.Nil || s.Source.UnitID == uuid.Nil || s.RootID == uuid.Nil || s.MediaID == uuid.Nil {
		return "", bad
	}
	if s.RootHealth != "online" || s.RootMediaType != s.MediaType || s.RootGeneration <= 0 || s.RootHealthRevision <= 0 || s.FileMappingRevision <= 0 || s.FileMutationSequence < 0 || s.FileFingerprint == "" || s.MediaMetadataVersion <= 0 || s.UnitMetadataVersion <= 0 || s.UnitFactsVersion <= 0 {
		return "", bad
	}
	if !filepath.IsAbs(s.RootPath) || filepath.Clean(s.RootPath) != s.RootPath || strings.ContainsAny(s.RootPath, "\x00\r\n") || !sameDirectory(s.RootIdentity, s.RootIdentity) || !validFile(s.FileIdentity) || s.FileIdentity.Device != s.RootIdentity.Device {
		return "", bad
	}
	if s.MarkerFilename != "" && (path.Base(s.MarkerFilename) != s.MarkerFilename || s.MarkerFilename == "." || s.MarkerFilename == ".." || strings.ContainsAny(s.MarkerFilename, "\\\x00\r\n")) {
		return "", bad
	}
	name, err := media.ChineseSubtitleName(s.FileRelative)
	if err != nil {
		return "", err
	}
	if len(s.Mappings) == 0 {
		return "", bad
	}
	seen := make(map[uuid.UUID]bool, len(s.Mappings))
	keys := make(map[string]bool, len(s.Mappings))
	found := false
	for _, m := range s.Mappings {
		if m.UnitID == uuid.Nil || m.MediaID != s.MediaID || seen[m.UnitID] || keys[m.UnitKey] || m.MetadataVersion <= 0 {
			return "", bad
		}
		seen[m.UnitID], keys[m.UnitKey] = true, true
		switch s.MediaType {
		case "movie":
			if len(s.Mappings) != 1 || m.UnitKey != "movie" || m.SeasonNumber != nil || m.EpisodeNumber != nil {
				return "", bad
			}
		case "tv":
			if !validEpisode(m.UnitKey, m.SeasonNumber, m.EpisodeNumber) || !sameNumber(m.SeasonNumber, s.SeasonNumber) {
				return "", bad
			}
		default:
			return "", bad
		}
		if m.UnitID == s.Source.UnitID {
			if m.UnitKey != s.UnitKey || !sameNumber(m.SeasonNumber, s.SeasonNumber) || !sameNumber(m.EpisodeNumber, s.EpisodeNumber) || m.MetadataVersion != s.UnitMetadataVersion {
				return "", bad
			}
			found = true
		}
	}
	if !found {
		return "", bad
	}
	return name, nil
}
func validEpisode(key string, season, episode *int32) bool {
	return season != nil && episode != nil && *season >= 0 && *season <= 999 && *episode >= 1 && *episode <= 9999 && key == fmt.Sprintf("S%02dE%02d", *season, *episode)
}
func sameNumber(a, b *int32) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func cloneNumber(n *int32) *int32 {
	if n == nil {
		return nil
	}
	v := *n
	return &v
}
func cloneSnapshot(s ports.MediaSidecarSnapshot) ports.MediaSidecarSnapshot {
	s.Year, s.SeasonNumber, s.EpisodeNumber = cloneNumber(s.Year), cloneNumber(s.SeasonNumber), cloneNumber(s.EpisodeNumber)
	s.Mappings = append([]ports.MediaSidecarMapping(nil), s.Mappings...)
	for i := range s.Mappings {
		s.Mappings[i].SeasonNumber = cloneNumber(s.Mappings[i].SeasonNumber)
		s.Mappings[i].EpisodeNumber = cloneNumber(s.Mappings[i].EpisodeNumber)
	}
	return s
}
