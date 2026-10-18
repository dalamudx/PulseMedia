package ports

import "context"

// IndexerPage carries provider pagination evidence for this window only.
// Missing offset or total evidence is unknown, even for an empty/short page.
type IndexerPage struct {
	Items          []*ReleaseItem
	Offset         int32
	Limit          int32
	OffsetVerified bool
	Total          *int64
}
type IndexerPageClient interface {
	SearchPage(context.Context, string, []int32, int32, int32) (*IndexerPage, error)
}
