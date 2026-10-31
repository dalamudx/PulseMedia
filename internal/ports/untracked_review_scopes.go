package ports

import "context"

type UntrackedReviewScopeOption struct {
	ID              string `json:"id"`
	Version         int64  `json:"version"`
	Enabled         bool   `json:"enabled"`
	LocalRoot       string `json:"local_root"`
	TargetRootPath  string `json:"target_root_path"`
	TargetMediaType string `json:"target_media_type"`
}

type UntrackedReviewScopePage struct {
	Items      []UntrackedReviewScopeOption `json:"items"`
	NextCursor string                       `json:"next_cursor"`
	HasMore    bool                         `json:"has_more"`
}

type UntrackedReviewScopeRepository interface {
	ReadUntrackedReviewScopes(context.Context, *Actor, string) (UntrackedReviewScopePage, error)
}
