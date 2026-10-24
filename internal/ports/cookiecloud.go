package ports

import "context"

// CookieCloudCookie preserves browser scope and unknown flags. Consumers must
// validate these facts against the configured endpoint before applying a cookie.
// This data contains secrets and must not be logged, cached or sent to HTTP APIs.
type CookieCloudCookie struct {
	Name           string   `json:"name"`
	Value          string   `json:"value"`
	Domain         string   `json:"domain"`
	Path           string   `json:"path"`
	HostOnly       *bool    `json:"hostOnly"`
	Secure         *bool    `json:"secure"`
	Session        *bool    `json:"session"`
	ExpirationDate *float64 `json:"expirationDate"`
	PartitionKey   any      `json:"partitionKey"`
}

type CookieCloudBackup struct {
	Cookies map[string][]CookieCloudCookie
}

func (CookieCloudBackup) String() string { return "CookieCloudBackup{redacted}" }
func (CookieCloudCookie) String() string { return "CookieCloudCookie{redacted}" }

type CookieCloudReader interface {
	ReadBackup(context.Context, string, string, string) (*CookieCloudBackup, error)
}
