package ports

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

var ErrCookieCloudConfigConflict = errors.New("CookieCloud configuration or command version changed")

// Connection and command secrets must not be logged or exposed in responses.
type CookieCloudConnection struct{ ServerURL, UserKey, Password string }

func (CookieCloudConnection) String() string { return "CookieCloudConnection{redacted}" }

type CookieCloudConfigInput struct {
	ExpectedVersion int64
	Enabled         bool
	IntervalSeconds int32
	Connection      *CookieCloudConnection
}

func (CookieCloudConfigInput) String() string { return "CookieCloudConfigInput{redacted}" }

type CookieCloudConfigView struct {
	OwnerID         uuid.UUID
	Version         int64
	Enabled         bool
	IntervalSeconds int32
	ServerOrigin    string
	HasConnection   bool
	UpdatedAt       time.Time
}
type CookieCloudConfigReceipt struct {
	CommandKey string
	Config     CookieCloudConfigView
}
type PreparedCookieCloudConfig struct {
	ExpectedVersion     int64
	Enabled             bool
	IntervalSeconds     int32
	ServerOrigin        string
	PreserveConnection  bool
	ConnectionEncrypted []byte `json:"-"`
}

func (PreparedCookieCloudConfig) String() string { return "PreparedCookieCloudConfig{redacted}" }

// Every operation rechecks the current administrator session and account scope.
// Receipt reads return nil,nil when absent. A nonnil digest requires exact
// same-key matching. Save locks account, key and configuration in that order,
// rechecks the version, and commits config/audit/event/original result together.
// Saving never creates a synchronization task or contacts a remote service.
type CookieCloudConfigRepository interface {
	ReadCookieCloudConfig(context.Context, *Actor) (*CookieCloudConfigView, error)
	ReadCookieCloudConfigReceipt(context.Context, *Actor, string, *string) (*CookieCloudConfigReceipt, error)
	SaveCookieCloudConfig(context.Context, *Actor, PreparedCookieCloudConfig, string, string) (*CookieCloudConfigReceipt, error)
}
