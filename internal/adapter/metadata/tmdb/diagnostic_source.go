package tmdb

import (
	"context"
	"crypto/sha256"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
)

type DiagnosticSettings interface {
	GetSetting(context.Context, string) (*ports.SettingItem, error)
	GetSecret(context.Context, string) (string, error)
}
type DiagnosticSource struct {
	settings    DiagnosticSettings
	environment func() string
	httpClient  *http.Client
	budget      ports.MetadataRequestBudget
}
type diagnosticBinding struct {
	version     int64
	digest      [32]byte
	environment bool
}
type diagnosticSession struct {
	source  *DiagnosticSource
	binding diagnosticBinding
	client  *Client
}

func NewDiagnosticSource(settings DiagnosticSettings, environment func() string, client *http.Client, budget ports.MetadataRequestBudget) *DiagnosticSource {
	return &DiagnosticSource{settings: settings, environment: environment, httpClient: client, budget: budget}
}
func (s *DiagnosticSource) read(ctx context.Context) (diagnosticBinding, string, error) {
	var binding diagnosticBinding
	if err := ctx.Err(); err != nil {
		return binding, "", err
	}
	secret := ""
	if s.environment != nil {
		secret = s.environment()
	}
	if secret != "" {
		binding.environment = true
	} else {
		if s.settings == nil {
			return binding, "", nil
		}
		before, err := s.settings.GetSetting(ctx, "tmdb.api_key")
		if err != nil {
			return binding, "", ports.ErrUnavailable
		}
		if before == nil {
			return binding, "", nil
		}
		if before.Key != "tmdb.api_key" || before.Version <= 0 {
			return binding, "", ports.ErrUnavailable
		}
		secret, err = s.settings.GetSecret(ctx, "tmdb.api_key")
		if err != nil {
			return binding, "", ports.ErrUnavailable
		}
		after, err := s.settings.GetSetting(ctx, "tmdb.api_key")
		if err != nil || after == nil || after.Key != before.Key || after.Version != before.Version {
			return binding, "", ports.ErrRejected
		}
		binding.version = before.Version
	}
	// Image diagnostics do not require an API credential; the API probe fails closed.
	binding.digest = sha256.Sum256([]byte(secret))
	return binding, secret, nil
}
func (s *DiagnosticSource) OpenDiagnostic(ctx context.Context) (ports.MetadataDiagnosticSession, error) {
	binding, secret, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	if s.budget == nil {
		return nil, ports.ErrUnavailable
	}
	opts := []Option{WithRequestBudget(s.budget)}
	if s.httpClient != nil {
		opts = append(opts, WithHTTPClient(s.httpClient))
	}
	session := &diagnosticSession{source: s, binding: binding, client: NewClient(secret, opts...)}
	if err := session.Validate(ctx); err != nil {
		return nil, err
	}
	return session, nil
}
func (s *diagnosticSession) Validate(ctx context.Context) error {
	current, _, err := s.source.read(ctx)
	if err != nil {
		return err
	}
	if current != s.binding {
		return ports.ErrRejected
	}
	return nil
}
func (s *diagnosticSession) Probe(ctx context.Context, target ports.MetadataDiagnosticTarget) ports.MetadataDiagnosticResult {
	return s.client.ProbeDiagnostic(ctx, target)
}
