package metadatasidecar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type ManagedUpdateService struct {
	repo     ports.ManagedSidecarUpdateRepository
	files    ports.ManagedSidecarUpdateFilesystem
	metadata ports.MediaSidecarMetadata
	artwork  ports.MetadataArtworkFetcher
	ids      ports.IDGen
}

func NewManagedUpdateService(repo ports.ManagedSidecarUpdateRepository, files ports.ManagedSidecarUpdateFilesystem, metadata ports.MediaSidecarMetadata, artwork ports.MetadataArtworkFetcher, ids ports.IDGen) *ManagedUpdateService {
	return &ManagedUpdateService{repo: repo, files: files, metadata: metadata, artwork: artwork, ids: ids}
}
func managedHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s *ManagedUpdateService) contents(ctx context.Context, snap ports.MediaSidecarSnapshot) ([]ports.ManagedSidecarContent, error) {
	if s.metadata == nil {
		return nil, ports.ErrUnavailable
	}
	year := 0
	if snap.Year != nil {
		year = int(*snap.Year)
	}
	var nfo []byte
	var poster string
	var e error
	name := "movie.nfo"
	if snap.MediaType == "movie" {
		d, err := s.metadata.GetMovieDetails(ctx, snap.TMDBID, "zh-CN")
		if err != nil {
			return nil, err
		}
		if d == nil || d.MediaType != "movie" || d.TMDBID != snap.TMDBID {
			return nil, ports.ErrMediaSidecarUnavailable
		}
		data := media.MovieNFOData{Title: snap.Title, OriginalTitle: snap.OriginalTitle, Year: year, TMDBID: snap.TMDBID, Plot: enrichmentText(d.Overview)}
		if data.Plot != d.Overview {
			return nil, ports.ErrMediaSidecarUnavailable
		}
		if validIMDb(d.IMDbID) {
			data.IMDbID = d.IMDbID
		}
		if len(d.Genres) > 128 {
			return nil, ports.ErrMediaSidecarUnavailable
		}
		for _, g := range d.Genres {
			v := enrichmentText(g)
			if v != g {
				return nil, ports.ErrMediaSidecarUnavailable
			}
			data.Genres = append(data.Genres, v)
		}
		nfo, e = media.GenerateMovieNFO(data)
		poster = media.TMDBPosterPath(d.PosterURL)
	} else {
		d, err := s.metadata.GetTVDetails(ctx, snap.TMDBID, "zh-CN")
		if err != nil {
			return nil, err
		}
		if d == nil || d.TMDBID != snap.TMDBID || enrichmentText(d.Overview) != d.Overview {
			return nil, ports.ErrMediaSidecarUnavailable
		}
		nfo, e = media.GenerateTVShowNFO(media.TVShowNFOData{Title: snap.Title, OriginalTitle: snap.OriginalTitle, Year: year, TMDBID: snap.TMDBID, Plot: d.Overview})
		poster = media.TMDBPosterPath(d.PosterURL)
		name = "tvshow.nfo"
	}
	if e != nil {
		return nil, e
	}
	if len(nfo) == 0 || len(nfo) > ports.MaxSidecarBytes {
		return nil, ports.ErrMediaSidecarResult
	}
	out := []ports.ManagedSidecarContent{{Name: name, Content: nfo, Hash: managedHash(nfo)}}
	if poster == "" {
		poster = media.TMDBPosterPath(media.TMDBPosterURL(snap.PosterPath))
	}
	if poster != "" && s.artwork != nil {
		b, err := s.artwork.FetchPoster(ctx, poster)
		if err != nil {
			return nil, err
		}
		if len(b) == 0 || len(b) > ports.MaxSidecarBytes {
			return nil, ports.ErrMediaSidecarResult
		}
		out = append(out, ports.ManagedSidecarContent{Name: "poster.jpg", Content: b, Hash: managedHash(b)})
	}
	return out, ctx.Err()
}
func (s *ManagedUpdateService) Preview(ctx context.Context, a *ports.Actor, file, unit uuid.UUID) (out ports.ManagedSidecarUpdateResult, err error) {
	if s.repo == nil || s.files == nil || s.ids == nil {
		return out, ports.ErrUnavailable
	}
	t, e := s.repo.LoadManaged(ctx, a, file, unit)
	if e != nil {
		return out, e
	}
	directory, _, e := workDirectory(t.Snapshot)
	if e != nil {
		return out, e
	}
	proposed, e := s.contents(ctx, t.Snapshot)
	if e != nil {
		return out, e
	}
	roots, release, e := s.files.LockRoots(ctx, t.Snapshot.RootPath)
	if e != nil {
		return out, e
	}
	defer release()
	root := roots[t.Snapshot.RootPath]
	if root == nil {
		return out, ports.ErrFileIdentity
	}
	observer := Service{files: s.files}
	dir, e := observer.observe(ctx, t.Snapshot, root, directory, nil)
	if e != nil {
		return out, e
	}
	out = ports.ManagedSidecarUpdateResult{ID: t.ID, FileID: file, State: "held", ExpiresAt: t.ExpiresAt, Artifacts: []ports.ManagedSidecarArtifact{}}
	trusted := t.Managed
	t.Managed = nil
	t.Proposed = nil
	for _, next := range proposed {
		item := ports.ManagedSidecarArtifact{Name: next.Name, State: "held", NewHash: next.Hash}
		for _, old := range trusted {
			if old.Name != next.Name || old.Directory != directory {
				continue
			}
			item.OldHash = old.Hash
			req := ports.SidecarRequest{SourceRelative: t.Snapshot.FileRelative, SourceIdentity: t.Snapshot.FileIdentity, DestinationDirectory: directory, DestinationIdentity: dir, Name: next.Name}
			observed, readErr := s.files.ReadManagedSidecar(ctx, root, req)
			if readErr != nil {
				if ctx.Err() != nil {
					return out, ctx.Err()
				}
				break
			}
			if observed.Identity != old.Identity || observed.Hash != old.Hash || managedHash(old.Content) != old.Hash || !bytes.Equal(observed.Content, old.Content) {
				break
			}
			if bytes.Equal(old.Content, next.Content) {
				item.State = "unchanged"
				break
			}
			item.State = "ready"
			out.State = "preview"
			next.Directory = directory
			t.Managed = append(t.Managed, old)
			t.Proposed = append(t.Proposed, next)
			break
		}
		out.Artifacts = append(out.Artifacts, item)
	}
	if _, e = observer.observe(ctx, t.Snapshot, root, directory, &dir); e != nil {
		return out, e
	}
	return s.repo.SaveManagedPreview(ctx, a, t, out)
}
func (s *ManagedUpdateService) Command(ctx context.Context, a *ports.Actor, file, preview uuid.UUID, key string) (out ports.ManagedSidecarUpdateResult, err error) {
	if s.repo == nil || s.files == nil {
		return out, ports.ErrUnavailable
	}
	t, out, e := s.repo.AuthorizeManagedUpdate(ctx, a, file, preview, key)
	if e != nil {
		return out, e
	}
	if t.Permit == uuid.Nil {
		return out, nil
	}
	roots, release, e := s.files.LockRoots(ctx, t.Snapshot.RootPath)
	if e != nil {
		return out, e
	}
	defer release()
	root := roots[t.Snapshot.RootPath]
	if root == nil {
		return out, ports.ErrFileIdentity
	}
	directory, _, e := workDirectory(t.Snapshot)
	if e != nil {
		return out, e
	}
	observer := Service{files: s.files}
	dir, e := observer.observe(ctx, t.Snapshot, root, directory, nil)
	if e != nil {
		return out, e
	}
	out.State = "completed"
	created := []ports.ManagedSidecarContent{}
	for i, next := range t.Proposed {
		if i >= len(t.Managed) {
			return out, ports.ErrMediaSidecarResult
		}
		if e = s.repo.ValidateManagedUpdate(ctx, a, t); e != nil {
			return out, e
		}
		if _, e = observer.observe(ctx, t.Snapshot, root, directory, &dir); e != nil {
			return out, e
		}
		req := ports.SidecarRequest{SourceRelative: t.Snapshot.FileRelative, SourceIdentity: t.Snapshot.FileIdentity, DestinationDirectory: directory, DestinationIdentity: dir, Name: next.Name, Content: append([]byte(nil), next.Content...)}
		result, callErr := s.files.ReplaceManagedSidecar(ctx, root, req, t.Managed[i], t.Permit)
		_, observeErr := observer.observe(ctx, t.Snapshot, root, directory, &dir)
		validErr := s.repo.ValidateManagedUpdate(ctx, a, t)
		if stop := errors.Join(callErr, observeErr, validErr, ctx.Err()); stop != nil || result.Disposition != ports.SidecarCreated || !result.Published {
			out.State = "review_required"
			for j := range out.Artifacts {
				if out.Artifacts[j].Name == next.Name {
					out.Artifacts[j].State = "review_required"
				}
			}
			if validErr != nil || ctx.Err() != nil {
				return out, errors.Join(stop, validErr, ctx.Err())
			}
			break
		}
		next.Identity = result.Identity
		created = append(created, next)
		for j := range out.Artifacts {
			if out.Artifacts[j].Name == next.Name {
				out.Artifacts[j].State = "updated"
			}
		}
	}
	for j := range out.Artifacts {
		if out.Artifacts[j].State == "ready" {
			out.Artifacts[j].State = "held"
		}
	}
	return s.repo.CompleteManagedUpdate(ctx, a, t, out, created)
}
func (s *ManagedUpdateService) ReadCommand(ctx context.Context, a *ports.Actor, file uuid.UUID, key string) (ports.ManagedSidecarUpdateResult, error) {
	if s.repo == nil {
		return ports.ManagedSidecarUpdateResult{}, ports.ErrUnavailable
	}
	return s.repo.ReadManagedUpdate(ctx, a, file, key)
}
