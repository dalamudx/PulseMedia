package ports

import (
	"context"
	"errors"
)

const CookieCloudSyncJobKind = "cookiecloud_sync"

var ErrCookieCloudSyncLeaseLost = errors.New("CookieCloud execution lease expired or changed")

// Execution secrets are process-local and must not be logged or serialized.
type CookieCloudExecution struct {
	Config     CookieCloudConfigView
	Connection CookieCloudConnection
}

func (CookieCloudExecution) String() string { return "CookieCloudExecution{redacted}" }

// Acquire validates and decrypts before authorizing network access. The permit
// retains physical exclusion until Close, including after logical lease loss.
// Reserve spends one read budget; Check verifies current account/config/lease.
// Finish applies only scope-valid cookies to existing supported sites, checking
// target versions, account/config/lease and expiry again at commit. Credentials,
// success audit/events and job result commit atomically. Failed/empty results
// never clear a credential or create a site, subscription or download task.
type CookieCloudSyncPermit interface {
	Snapshot() CookieCloudExecution
	Reserve(context.Context) error
	Check(context.Context) error
	Finish(context.Context, *CookieCloudBackup, error) error
	Close()
}
type CookieCloudSyncRepository interface {
	EnqueueDue(context.Context) error
	RecoverExpired(context.Context) error
	Acquire(context.Context, JobLease) (CookieCloudSyncPermit, error)
}
