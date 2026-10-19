package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// EndpointFamily identifies persisted configuration, not execution authority.
type EndpointFamily string

const (
	DownloadClientEndpoint EndpointFamily = "download_client"
	MediaServerEndpoint    EndpointFamily = "media_server"
)

var (
	ErrInvalidEndpointConfiguration = errors.New("invalid endpoint configuration")
	ErrEndpointVersionConflict      = errors.New("endpoint configuration changed or version missing")
	ErrEndpointInUse                = errors.New("endpoint configuration is referenced")
	ErrEndpointProbeFailed          = errors.New("endpoint connection probe failed")
)

// EndpointConfiguration is a safe read projection. URL must be redacted by the repository.
type EndpointConfiguration struct {
	ID        uuid.UUID
	Family    EndpointFamily
	Name      string
	Protocol  string
	URL       string
	Username  string
	Health    string
	UpdatedAt time.Time
}

// Secret and complete URL are transient inputs. Empty Secret preserves existing
// ciphertext on update; creation encrypts even an empty credential. Mapping is
// accepted on creation only, retaining existing frozen mapping semantics.
type EndpointConfigurationInput struct {
	Name              string
	Protocol          string
	URL               string `json:"-"`
	Username          string
	Secret            string          `json:"-"`
	Mapping           json.RawMessage `json:"-"`
	ExpectedUpdatedAt time.Time
}

// EndpointProbeScope binds one authorized read-only probe to exact current
// configuration. ConfigStamp is opaque and is revalidated after I/O.
type EndpointProbeScope struct {
	Family      EndpointFamily
	ID          uuid.UUID
	UpdatedAt   time.Time
	ConfigStamp string `json:"-"`
}

// EndpointConfigurationRepository owns narrow transactions. Writes and probes
// require a current active administrator with no token scopes, rechecked after
// locking the account; all reads validate the current active database account.
// Audit/outbox for successful writes and validated probes commit atomically.
// Methods never perform network I/O or return credential-bearing configuration.
type EndpointConfigurationRepository interface {
	ListEndpointConfigurations(context.Context, *Actor, EndpointFamily) ([]EndpointConfiguration, error)
	CreateEndpointConfiguration(context.Context, *Actor, EndpointFamily, EndpointConfigurationInput) (*EndpointConfiguration, error)
	UpdateEndpointConfiguration(context.Context, *Actor, EndpointFamily, uuid.UUID, EndpointConfigurationInput) (*EndpointConfiguration, error)
	DeleteEndpointConfiguration(context.Context, *Actor, EndpointFamily, uuid.UUID) error
	AuthorizeEndpointProbe(context.Context, *Actor, EndpointFamily, uuid.UUID) (*EndpointProbeScope, error)
	CompleteEndpointProbe(context.Context, *Actor, EndpointProbeScope, bool) error
}

// Narrow connectivity contracts preserve native client construction outside the
// management transaction. Native failure uses ErrEndpointProbeFailed; storage,
// missing configuration and unavailable credentials retain distinct safe errors.
type DownloadEndpointTester interface {
	TestClient(context.Context, uuid.UUID) (bool, string, error)
}

type MediaEndpointTester interface {
	TestServer(context.Context, uuid.UUID) (bool, string, error)
}

type EndpointProbeResult struct {
	Alive   bool
	Message string
}
