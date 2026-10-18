package ports

import "context"

// LocalMediaSearchReader supplies the authenticated catalog fallback when the
// external metadata service has not been configured. It returns no paths or secrets.
type LocalMediaSearchReader interface {
	SearchLocalMedia(context.Context, *Actor, string, string, int) ([]*MovieMetadata, error)
}
