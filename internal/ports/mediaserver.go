package ports

import (
	"context"
)

// MediaServerClient abstracts communication with media servers like Jellyfin, Emby, or Plex.
type MediaServerClient interface {
	RefreshPaths(ctx context.Context, paths []string) error
	FindByPath(ctx context.Context, path string) (itemID string, found bool, err error)
	Test(ctx context.Context) error
}
