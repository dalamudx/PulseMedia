package ports

import "context"

// WatchlistIdentityMetadata separates complete, typed identity lookup from the
// browse-only SearchMedia array. MovieIdentitySearch already carries generic
// movie/TV metadata; its historical name does not change the completeness rule.
type WatchlistIdentityMetadata interface {
	SearchMediaIdentityCandidates(context.Context, string, string, int) (MovieIdentitySearch, error)
	GetMovieDetails(context.Context, int64, string) (*MovieMetadata, error)
	GetTVDetails(context.Context, int64, string) (*TVDetails, error)
}
