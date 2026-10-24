package metadatasidecar

import (
	"bytes"
	"context"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Optional artwork failures do not suppress canonical NFO/poster sidecars. All
// network I/O ends before taking the root slot; every actual write still uses
// the existing observation/lease fence and no-overwrite publication protocol.
func (s *Service) artworkContents(ctx context.Context, snapshot ports.MediaSidecarSnapshot, provider ports.MediaArtworkProvider) ([]ports.SidecarRequest, error) {
	identity := ports.ArtworkIdentity{MediaType: snapshot.MediaType, Provider: "tmdb", ID: snapshot.TMDBID}
	if snapshot.MediaType == "tv" {
		if s.tvIdentity == nil {
			return nil, nil
		}
		pair, err := s.tvIdentity.ResolveTVArtworkIdentity(ctx, snapshot.TMDBID)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil || pair.TMDBID != snapshot.TMDBID || pair.TVDBID <= 0 {
			return nil, nil
		}
		identity.Provider, identity.ID = "tvdb", pair.TVDBID
	}
	art, err := provider.GetArtworks(ctx, identity)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || art.Identity != identity {
		return nil, nil
	}
	candidates := []struct{ name, reference string }{{"logo.png", art.ClearLogo}, {"clearart.png", art.ClearArt}, {"banner.jpg", art.Banner}}
	if snapshot.MediaType == "movie" {
		candidates = append(candidates, struct{ name, reference string }{"disc.png", art.DiscArt})
	}
	out := make([]ports.SidecarRequest, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.reference == "" {
			continue
		}
		content, err := provider.FetchArtwork(ctx, candidate.reference)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, limited := ports.IsRateLimited(err); limited {
			return out, nil
		}
		if err != nil || len(content) == 0 || len(content) > ports.MaxSidecarBytes {
			continue
		}
		out = append(out, ports.SidecarRequest{Name: candidate.name, Content: bytes.Clone(content)})
	}
	return out, nil
}
