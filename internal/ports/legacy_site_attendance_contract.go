package ports

import "context"

type SiteCheckinResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type SiteAttendanceClient interface {
	CheckinSite(ctx context.Context, siteURL, cookie string) (*SiteCheckinResult, error)
}
