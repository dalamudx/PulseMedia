package ports

import "context"

// ArtworkIdentity is a proven provider identity, never a local UUID or search result.
// Fanart movies require TMDB; TV requires TVDB, which cannot be inferred from TMDB.
type ArtworkIdentity struct {
	MediaType string
	Provider  string
	ID        int64
}

// MediaArtworks holds provider-relative image references, not caller supplied URLs.
// Empty fields mean the successful response contained no image of that kind.
type MediaArtworks struct {
	Identity  ArtworkIdentity
	ClearLogo string
	ClearArt  string
	Banner    string
	DiscArt   string
}

type MediaArtworkProvider interface {
	GetArtworks(context.Context, ArtworkIdentity) (MediaArtworks, error)
	FetchArtwork(context.Context, string) ([]byte, error)
}
