package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"os"
)

// MovieRecognizer owns the physical root slot across metadata I/O and writeback.
type MovieRecognizer struct {
	files    ports.RootFilesystem
	repo     ports.CheckedMovieRecognitionRepository
	provider ports.MovieIdentityProvider
}

func NewMovieRecognizer(files ports.RootFilesystem, repo ports.CheckedMovieRecognitionRepository, provider ports.MovieIdentityProvider) *MovieRecognizer {
	return &MovieRecognizer{files: files, repo: repo, provider: provider}
}
func (r *MovieRecognizer) Recognize(ctx context.Context, rootID uuid.UUID, relPath string) error {
	source, err := r.repo.LoadSource(ctx, rootID, relPath)
	if err != nil {
		return r.resolve(ctx, err)
	}
	roots, release, err := r.files.LockRoots(ctx, source.Root.Path)
	if err != nil {
		return r.resolve(ctx, fmt.Errorf("%w: %w", ports.ErrMoviePhysicalCheck, err))
	}
	defer release()
	checked := &checkedMovieRepository{repo: r.repo, files: r.files, root: roots[source.Root.Path], source: source}
	_, err = NewMatcher(checked, r.provider).MatchAndLinkFile(ctx, rootID, relPath)
	return r.resolve(ctx, err)
}
func (r *MovieRecognizer) resolve(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ports.ErrMovieRecognitionLease) {
		return err
	}
	if _, ok := ports.IsRateLimited(err); ok {
		return err
	}
	if errors.Is(err, ports.ErrUnavailable) || errors.Is(err, ports.ErrTimeout) || errors.Is(err, ports.ErrAuthRequired) {
		return err
	}
	reason := ""
	switch {
	case errors.Is(err, ErrLowConfidence):
		reason = "low_confidence"
	case errors.Is(err, ports.ErrMoviePhysicalCheck), errors.Is(err, os.ErrNotExist), errors.Is(err, os.ErrPermission), errors.Is(err, ports.ErrFileSymlink), errors.Is(err, ports.ErrFileEscape):
		reason = "source_unavailable"
	case errors.Is(err, ports.ErrMovieLinkStale):
		reason = "source_changed"
	case errors.Is(err, ports.ErrMovieLinkConflict):
		reason = "mapping_conflict"
	case errors.Is(err, ports.ErrBadResponse):
		reason = "invalid_response"
	case errors.Is(err, ErrNoMetadataMatch), errors.Is(err, ports.ErrRejected):
		reason = "identity_unconfirmed"
	}
	if reason == "" {
		return err
	}
	return r.repo.Review(ctx, reason)
}

type checkedMovieRepository struct {
	repo   ports.CheckedMovieRecognitionRepository
	files  ports.RootFilesystem
	root   ports.TransferRoot
	source ports.MovieFileSnapshot
}

func (r *checkedMovieRepository) LoadSource(ctx context.Context, rootID uuid.UUID, path string) (ports.MovieFileSnapshot, error) {
	source, err := r.repo.LoadSource(ctx, rootID, path)
	if err != nil {
		return source, err
	}
	frozen := r.source
	if source.Root != frozen.Root || source.FileID != frozen.FileID || source.RelPath != frozen.RelPath || source.SizeBytes != frozen.SizeBytes || source.MtimeNS != frozen.MtimeNS || source.Fingerprint != frozen.Fingerprint || source.FingerprintKnown != frozen.FingerprintKnown || source.MappingRevision != frozen.MappingRevision || source.MutationSeq != frozen.MutationSeq || !bytes.Equal(source.MappingStamp, frozen.MappingStamp) {
		return source, ports.ErrMovieLinkStale
	}
	_, err = r.check(ctx, source)
	return source, err
}
func (r *checkedMovieRepository) check(ctx context.Context, source ports.MovieFileSnapshot) (ports.MovieFileCheck, error) {
	proof, err := r.repo.BeginFileCheck(ctx, source)
	if err != nil {
		return proof, err
	}
	if err = r.verify(ctx, source, proof); err != nil {
		return proof, fmt.Errorf("%w: %w", ports.ErrMoviePhysicalCheck, err)
	}
	stat, err := r.root.Inspect(ctx, source.RelPath)
	if err != nil {
		return proof, fmt.Errorf("%w: %w", ports.ErrMoviePhysicalCheck, err)
	}
	fingerprint := fmt.Sprintf("stat.v1:%d:%d:%d", stat.Inode, stat.Size, stat.Mtime)
	if !source.FingerprintKnown || source.Fingerprint != fingerprint || source.SizeBytes != stat.Size || source.MtimeNS != stat.Mtime || stat.Device != proof.RootIdentity.Device {
		return proof, ports.ErrMoviePhysicalCheck
	}
	if err = r.verify(ctx, source, proof); err != nil {
		return proof, fmt.Errorf("%w: %w", ports.ErrMoviePhysicalCheck, err)
	}
	return proof, nil
}
func (r *checkedMovieRepository) verify(ctx context.Context, source ports.MovieFileSnapshot, proof ports.MovieFileCheck) error {
	current, err := r.files.OpenRoot(ctx, source.Root.Path)
	if err != nil {
		return err
	}
	defer current.Close()
	original, err := r.root.Identity(ctx)
	if err != nil {
		return err
	}
	actual, err := current.Identity(ctx)
	if err != nil {
		return err
	}
	if actual.Device != proof.RootIdentity.Device || actual.Inode != proof.RootIdentity.Inode || original.Device != actual.Device || original.Inode != actual.Inode {
		return ports.ErrFileIdentity
	}
	marker := source.Root.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	_, err = current.Inspect(ctx, marker)
	return err
}
func (r *checkedMovieRepository) Link(ctx context.Context, in ports.MovieLinkInput) (ports.MovieLinkResult, error) {
	proof, err := r.check(ctx, in.Source)
	if err != nil {
		return ports.MovieLinkResult{}, err
	}
	in.PhysicalCheck = &proof
	return r.repo.Link(ctx, in)
}
