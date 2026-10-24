package ports

import "context"

// TVArtworkIdentity is read-only evidence from the exact TMDB TV external_ids
// response. It authorizes artwork lookup only, never a subscription or download.
// It is separate from persisted/frozen TVDetails JSON.
type TVArtworkIdentity struct {
	TMDBID int64
	TVDBID int64
}

type TVArtworkIdentityResolver interface {
	ResolveTVArtworkIdentity(context.Context, int64) (TVArtworkIdentity, error)
}
