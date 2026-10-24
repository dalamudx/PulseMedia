package metadatasidecar

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Service runs one synchronous, lease-fenced sidecar continuation.
type Service struct {
	repo          ports.MediaSidecarRepository
	files         ports.MediaSidecarFilesystem
	metadata      ports.MediaSidecarMetadata
	artwork       ports.MetadataArtworkFetcher
	artworks      ports.MediaArtworkProvider
	tvIdentity    ports.TVArtworkIdentityResolver
	artworkSource ports.MediaArtworkSource
}

type Option func(*Service)

// WithArtworks enables optional artwork enrichment without changing legacy jobs.
func WithArtworks(provider ports.MediaArtworkProvider, tv ports.TVArtworkIdentityResolver) Option {
	return func(s *Service) { s.artworks, s.tvIdentity = provider, tv }
}

// WithArtworkSource gives each execution a distinct configuration snapshot.
func WithArtworkSource(source ports.MediaArtworkSource, tv ports.TVArtworkIdentityResolver) Option {
	return func(s *Service) { s.artworkSource, s.tvIdentity = source, tv }
}

func NewService(repo ports.MediaSidecarRepository, files ports.MediaSidecarFilesystem, metadata ports.MediaSidecarMetadata, artwork ports.MetadataArtworkFetcher, options ...Option) *Service {
	s := &Service{repo: repo, files: files, metadata: metadata, artwork: artwork}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *Service) Execute(ctx context.Context, lease ports.JobLease) error {
	snapshot, err := s.repo.Load(ctx, lease)
	if err != nil {
		return err
	}
	snapshot = cloneSnapshot(snapshot)
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, name, err := workDirectory(snapshot)
	if err != nil {
		return err
	}
	var extra []ports.SidecarRequest
	provider := s.artworks
	var artworkSession ports.MediaArtworkSession
	if s.artworkSource != nil {
		provider = nil
		if session, openErr := s.artworkSource.Open(ctx); openErr == nil && session != nil {
			artworkSession, provider = session, session
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if provider != nil {
		if err := s.validate(ctx, lease, snapshot); err != nil {
			return err
		}
		extra, err = s.artworkContents(ctx, snapshot, provider)
		if err != nil {
			return err
		}
	}
	nfo, poster, err := s.contents(ctx, snapshot)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	roots, release, err := s.files.LockRoots(ctx, snapshot.RootPath)
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	root := roots[snapshot.RootPath]
	if root == nil {
		return ports.ErrFileIdentity
	}
	// Even a waiter must recheck the owner/token/lease before native observations.
	if err := s.validate(ctx, lease, snapshot); err != nil {
		return err
	}
	dirID, err := s.observe(ctx, snapshot, root, directory, nil)
	if err != nil {
		return err
	}

	artifacts := []ports.SidecarRequest{{Name: name, Content: nfo}}
	if len(poster) > 0 {
		artifacts = append(artifacts, ports.SidecarRequest{Name: "poster.jpg", Content: poster})
	}
	artifacts = append(artifacts, extra...)
	completion := ports.MediaSidecarCompletion{}
	for _, artifact := range artifacts {
		if artworkSession != nil && artifact.Name != name && artifact.Name != "poster.jpg" {
			if err := artworkSession.Validate(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				break
			}
		}
		if _, err := s.observe(ctx, snapshot, root, directory, &dirID); err != nil {
			return err
		}
		if err := s.validate(ctx, lease, snapshot); err != nil {
			return err
		}
		artifact.SourceRelative = snapshot.FileRelative
		artifact.SourceIdentity = snapshot.FileIdentity
		artifact.DestinationDirectory = directory
		artifact.DestinationIdentity = dirID
		artifact.Content = append([]byte(nil), artifact.Content...)
		result, publishErr := s.files.PublishSidecar(ctx, root, artifact)
		completion.Artifacts = append(completion.Artifacts, ports.MediaSidecarArtifactResult{Name: artifact.Name, Result: result})
		// No cancellation wrapper: the physical slot is held until the call returns.
		if err := ctx.Err(); err != nil {
			return errors.Join(publishErr, err)
		}
		_, observeErr := s.observe(ctx, snapshot, root, directory, &dirID)
		validateErr := s.validate(ctx, lease, snapshot)
		outcomeErr := publicationError(result)
		if stopErr := errors.Join(publishErr, observeErr, validateErr, outcomeErr); stopErr != nil {
			if validateErr != nil {
				return stopErr
			}
			// A successful native result cannot prove completion after a failed fence.
			if (publishErr != nil || observeErr != nil) && outcomeErr == nil {
				return stopErr
			}
			// Persist the actual outcome, including unsafe/uncertain, without replay.
			return errors.Join(stopErr, s.complete(ctx, lease, snapshot, completion))
		}
	}
	return s.complete(ctx, lease, snapshot, completion)
}

func (s *Service) validate(ctx context.Context, lease ports.JobLease, snapshot ports.MediaSidecarSnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.repo.Validate(ctx, lease, cloneSnapshot(snapshot))
	return errors.Join(err, ctx.Err())
}

func (s *Service) complete(ctx context.Context, lease ports.JobLease, snapshot ports.MediaSidecarSnapshot, completion ports.MediaSidecarCompletion) error {
	if err := s.validate(ctx, lease, snapshot); err != nil {
		return err
	}
	err := s.repo.Complete(ctx, lease, cloneSnapshot(snapshot), completion)
	return errors.Join(err, ctx.Err())
}

func publicationError(result ports.SidecarResult) error {
	switch result.Disposition {
	case ports.SidecarCreated:
		if result.Published {
			return nil
		}
	case ports.SidecarPreservedRegular:
		if !result.Published {
			return nil
		}
	}
	return ports.ErrMediaSidecarResult
}

// observe reopens the configured namespace rather than trusting a pinned FD.
func (s *Service) observe(ctx context.Context, snapshot ports.MediaSidecarSnapshot, root ports.TransferRoot, directory string, expected *ports.SidecarDirectoryIdentity) (ports.SidecarDirectoryIdentity, error) {
	var zero ports.SidecarDirectoryIdentity
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	id, err := root.Identity(ctx)
	if err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !sameDirectory(id, snapshot.RootIdentity) {
		return zero, ports.ErrFileIdentity
	}
	namespace, err := s.files.OpenRoot(ctx, snapshot.RootPath)
	if err != nil {
		return zero, err
	}
	defer namespace.Close()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	id, err = namespace.Identity(ctx)
	if err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !sameDirectory(id, snapshot.RootIdentity) {
		return zero, ports.ErrFileIdentity
	}
	marker := snapshot.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	if _, err := namespace.Inspect(ctx, marker); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	id, err = root.Inspect(ctx, snapshot.FileRelative)
	if err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if id != snapshot.FileIdentity || id.Device != snapshot.RootIdentity.Device {
		return zero, ports.ErrFileIdentity
	}
	dirID, err := s.files.InspectSidecarDirectory(ctx, root, directory)
	if err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if dirID.Device != snapshot.RootIdentity.Device || dirID.Inode <= 0 || (expected != nil && dirID != *expected) {
		return zero, ports.ErrFileIdentity
	}
	return dirID, nil
}

func sameDirectory(a, b ports.FileIdentity) bool {
	return b.Device > 0 && b.Inode > 0 && a.Device == b.Device && a.Inode == b.Inode
}

func workDirectory(s ports.MediaSidecarSnapshot) (string, string, error) {
	bad := ports.ErrMediaSidecarUnavailable
	if s.MediaID == uuid.Nil || s.Source.UnitID == uuid.Nil || s.TMDBID <= 0 || strings.TrimSpace(s.Title) == "" || strings.ContainsAny(s.Title, "\x00\r\n") || s.RootHealth != "online" || s.RootMediaType != s.MediaType {
		return "", "", bad
	}
	if !filepath.IsAbs(s.RootPath) || filepath.Clean(s.RootPath) != s.RootPath || !sameDirectory(s.RootIdentity, s.RootIdentity) || s.FileIdentity.Inode <= 0 || s.FileIdentity.Device != s.RootIdentity.Device {
		return "", "", bad
	}
	if s.MarkerFilename != "" && (filepath.Base(s.MarkerFilename) != s.MarkerFilename || s.MarkerFilename == "." || s.MarkerFilename == ".." || strings.ContainsAny(s.MarkerFilename, "\\\x00")) {
		return "", "", bad
	}
	year := 0
	if s.Year != nil {
		year = int(*s.Year)
	}
	if year < 0 || year > 9999 {
		return "", "", bad
	}
	directory := naming.FormatMovieDir(s.Title, year)
	if clean, err := naming.CleanRelPath(directory); err != nil || clean != directory || directory == "." || directory == "" || filepath.Base(directory) != directory {
		return "", "", bad
	}
	clean, err := naming.CleanRelPath(s.FileRelative)
	if err != nil || clean != s.FileRelative || filepath.Base(clean) == "." {
		return "", "", bad
	}
	parent := directory
	name := "movie.nfo"
	switch s.MediaType {
	case "movie":
		if s.UnitKey != "movie" || s.SeasonNumber != nil || s.EpisodeNumber != nil {
			return "", "", bad
		}
	case "tv":
		if !validEpisode(s.UnitKey, s.SeasonNumber, s.EpisodeNumber) {
			return "", "", bad
		}
		parent = filepath.Join(directory, fmt.Sprintf("Season %02d", *s.SeasonNumber))
		name = "tvshow.nfo"
	default:
		return "", "", bad
	}
	if filepath.Dir(clean) != parent || len(s.Mappings) == 0 {
		return "", "", bad
	}
	seen := make(map[uuid.UUID]bool, len(s.Mappings))
	found := false
	for _, m := range s.Mappings {
		if m.MediaID != s.MediaID || m.UnitID == uuid.Nil || seen[m.UnitID] {
			return "", "", bad
		}
		seen[m.UnitID] = true
		if s.MediaType == "movie" {
			if m.UnitKey != "movie" || m.SeasonNumber != nil || m.EpisodeNumber != nil || len(s.Mappings) != 1 {
				return "", "", bad
			}
		} else if !validEpisode(m.UnitKey, m.SeasonNumber, m.EpisodeNumber) || *m.SeasonNumber != *s.SeasonNumber {
			return "", "", bad
		}
		if m.UnitID == s.Source.UnitID {
			if m.UnitKey != s.UnitKey || !sameNumber(m.SeasonNumber, s.SeasonNumber) || !sameNumber(m.EpisodeNumber, s.EpisodeNumber) {
				return "", "", bad
			}
			found = true
		}
	}
	if !found {
		return "", "", bad
	}
	return directory, name, nil
}

func validEpisode(key string, season, episode *int32) bool {
	return season != nil && episode != nil && *season >= 0 && *season <= 999 && *episode >= 1 && *episode <= 9999 && key == media.FormatEpisodeUnitKey(int(*season), int(*episode))
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

func (s *Service) contents(ctx context.Context, snapshot ports.MediaSidecarSnapshot) ([]byte, []byte, error) {
	year := 0
	if snapshot.Year != nil {
		year = int(*snapshot.Year)
	}
	movie := media.MovieNFOData{Title: snapshot.Title, OriginalTitle: snapshot.OriginalTitle, Year: year, TMDBID: snapshot.TMDBID}
	tv := media.TVShowNFOData{Title: snapshot.Title, OriginalTitle: snapshot.OriginalTitle, Year: year, TMDBID: snapshot.TMDBID}
	posterPath := media.TMDBPosterPath(media.TMDBPosterURL(snapshot.PosterPath))
	if s.metadata != nil {
		if snapshot.MediaType == "movie" {
			details, err := s.metadata.GetMovieDetails(ctx, snapshot.TMDBID, "zh-CN")
			if err != nil {
				posterPath = ""
			} else if details != nil && details.MediaType == "movie" && details.TMDBID == snapshot.TMDBID {
				movie.Plot = enrichmentText(details.Overview)
				if validIMDb(details.IMDbID) {
					movie.IMDbID = details.IMDbID
				}
				if len(details.Genres) <= 128 {
					for _, genre := range details.Genres {
						if text := enrichmentText(genre); text != "" {
							movie.Genres = append(movie.Genres, text)
						}
					}
				}
				if posterPath == "" {
					posterPath = media.TMDBPosterPath(details.PosterURL)
				}
			}
		} else {
			details, err := s.metadata.GetTVDetails(ctx, snapshot.TMDBID, "zh-CN")
			if err != nil {
				posterPath = ""
			} else if details != nil && details.TMDBID == snapshot.TMDBID {
				tv.Plot = enrichmentText(details.Overview)
				if posterPath == "" {
					posterPath = media.TMDBPosterPath(details.PosterURL)
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var nfo []byte
	var err error
	if snapshot.MediaType == "movie" {
		nfo, err = media.GenerateMovieNFO(movie)
	} else {
		nfo, err = media.GenerateTVShowNFO(tv)
	}
	// Malformed optional provider text must not prevent the canonical NFO.
	if err != nil || len(nfo) > ports.MaxSidecarBytes {
		movie.Plot, movie.IMDbID, movie.Genres, tv.Plot = "", "", nil, ""
		if snapshot.MediaType == "movie" {
			nfo, err = media.GenerateMovieNFO(movie)
		} else {
			nfo, err = media.GenerateTVShowNFO(tv)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if len(nfo) == 0 || len(nfo) > ports.MaxSidecarBytes {
		return nil, nil, ports.ErrSidecarRequest
	}
	var poster []byte
	if posterPath != "" && s.artwork != nil {
		data, fetchErr := s.artwork.FetchPoster(ctx, posterPath)
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if fetchErr == nil && len(data) > 0 && len(data) <= ports.MaxSidecarBytes {
			poster = append([]byte(nil), data...)
		}
	}
	return nfo, poster, ctx.Err()
}

func enrichmentText(text string) string {
	if len(text) > 64*1024 || strings.Contains(text, "://") || strings.Contains(text, "//") {
		return ""
	}
	return text
}

func validIMDb(id string) bool {
	if len(id) < 3 || len(id) > 20 || !strings.HasPrefix(id, "tt") {
		return false
	}
	for _, r := range id[2:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
