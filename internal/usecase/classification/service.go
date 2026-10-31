package classification

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/domain/classification"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"time"
)

type Service struct {
	repo     ports.ClassificationRepository
	provider ports.MetadataProvider
}

func NewService(repo ports.ClassificationRepository) *Service { return &Service{repo: repo} }
func NewServiceWithMetadata(repo ports.ClassificationRepository, provider ports.MetadataProvider) *Service {
	return &Service{repo: repo, provider: provider}
}
func (s *Service) Read(ctx context.Context, a *ports.Actor) (*ports.ClassificationDocument, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	return s.repo.ReadClassification(ctx, a)
}
func (s *Service) Publish(ctx context.Context, a *ports.Actor, in ports.ClassificationPublish, key string) (*ports.ClassificationDocument, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if in.ExpectedVersion < 0 || classification.Validate(in.Rules) != nil || !validKey(key) {
		return nil, ports.ErrInvalidClassification
	}
	if in.Rules == nil {
		in.Rules = []classification.Rule{}
	}
	return s.repo.PublishClassification(ctx, a, in, key)
}
func (s *Service) Preview(ctx context.Context, a *ports.Actor, in ports.ClassificationPreviewInput) (*ports.ClassificationPreview, error) {
	if a == nil {
		return nil, ports.ErrUnauthorized
	}
	if s == nil || s.repo == nil {
		return nil, ports.ErrUnavailable
	}
	if in.Version < 1 || (in.MediaID == nil) == (in.TMDBID == nil) || in.TMDBID != nil && (*in.TMDBID < 1 || (in.MediaType != "movie" && in.MediaType != "tv")) {
		return nil, ports.ErrInvalidClassification
	}
	actor := *a
	a = &actor
	if in.TMDBID != nil {
		id := *in.TMDBID
		in.TMDBID = &id
	}
	if in.MediaID != nil {
		id := *in.MediaID
		in.MediaID = &id
	}
	in.TrustedFacts = nil
	if in.TMDBID != nil && s.provider != nil {
		if a.Type != ports.ActorTypeUser || a.Scopes != nil {
			return nil, ports.ErrForbidden
		}
		preparer, ok := s.repo.(interface {
			AuthorizeClassificationPreview(context.Context, *ports.Actor, ports.ClassificationPreviewInput) error
		})
		if !ok {
			return nil, ports.ErrUnavailable
		}
		if e := preparer.AuthorizeClassificationPreview(ctx, a, in); e != nil {
			return nil, e
		}
		network, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		var facts classification.Facts
		var e error
		if in.MediaType == "movie" {
			var d *ports.MovieMetadata
			d, e = s.provider.GetMovieDetails(network, *in.TMDBID, "zh-CN")
			if e == nil {
				if d == nil || d.TMDBID != *in.TMDBID || d.MediaType != "movie" {
					return nil, ports.ErrBadResponse
				}
				facts, e = classification.MetadataFacts("movie", d.Genres, d.OriginalLanguage, d.Countries)
			}
		} else {
			var d *ports.TVDetails
			d, e = s.provider.GetTVDetails(network, *in.TMDBID, "zh-CN")
			if e == nil {
				if d == nil || d.TMDBID != *in.TMDBID {
					return nil, ports.ErrBadResponse
				}
				facts, e = classification.MetadataFacts("tv", d.Genres, d.OriginalLanguage, d.Countries)
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if network.Err() != nil {
			return nil, ports.ErrUnavailable
		}
		if e != nil {
			return nil, ports.ErrUnavailable
		}
		in.TrustedFacts = &facts
	}
	return s.repo.PreviewClassification(ctx, a, in)
}
func validKey(k string) bool {
	if len(k) < 1 || len(k) > 160 {
		return false
	}
	for _, c := range k {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' && c != ':' {
			return false
		}
	}
	return true
}
