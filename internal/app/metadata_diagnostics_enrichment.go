package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/fanart"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tvdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// The catalogue opens only the selected source, so broken optional configuration
// cannot prevent a TMDB diagnostic. Each request owns its snapshot and transport.
type metadataDiagnosticCatalogue struct{ provider *dynamicTMDBProvider }

func newMetadataDiagnosticCatalogue(p *dynamicTMDBProvider) ports.MetadataDiagnosticSource {
	return metadataDiagnosticCatalogue{provider: p}
}
func (s metadataDiagnosticCatalogue) OpenDiagnostic(ctx context.Context) (ports.MetadataDiagnosticSession, error) {
	if s.provider == nil {
		return nil, ports.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &enrichmentDiagnosticSession{provider: s.provider}, nil
}

type enrichmentDiagnosticSession struct {
	provider *dynamicTMDBProvider
	validate func(context.Context) error
}

func (s *enrichmentDiagnosticSession) Validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.validate != nil {
		return s.validate(ctx)
	}
	return nil
}
func (s *enrichmentDiagnosticSession) Probe(ctx context.Context, target ports.MetadataDiagnosticTarget) ports.MetadataDiagnosticResult {
	result := ports.MetadataDiagnosticResult{Target: target, Category: "unavailable"}
	p := s.provider
	if target == ports.DiagnosticTMDBAPI || target == ports.DiagnosticTMDBImage {
		session, err := (metadataDiagnosticSource{provider: p}).OpenDiagnostic(ctx)
		if err != nil {
			return result
		}
		s.validate = session.Validate
		if s.Validate(ctx) != nil {
			result.Category = "configuration_changed"
			return result
		}
		return session.Probe(ctx, target)
	}
	if p.settingsSvc == nil {
		result.Category = "not_configured"
		return result
	}
	var probe func(context.Context, *http.Client) error
	switch target {
	case ports.DiagnosticFanartAPI:
		source := artworkSource{settings: p.settingsSvc}
		binding, key, err := source.read(ctx)
		if err != nil {
			return diagnosticOpenFailure(result, err)
		}
		s.validate = func(ctx context.Context) error {
			current, _, err := source.read(ctx)
			if err != nil || current != binding {
				return ports.ErrRejected
			}
			return nil
		}
		probe = func(ctx context.Context, client *http.Client) error {
			_, err := fanart.NewClient(key, client).GetArtworks(ctx, ports.ArtworkIdentity{MediaType: "movie", Provider: "tmdb", ID: 550})
			return err
		}
	case ports.DiagnosticTVDBAPI:
		source := tvdbSource{settings: p.settingsSvc}
		binding, credentials, err := source.read(ctx)
		if err != nil {
			return diagnosticOpenFailure(result, err)
		}
		s.validate = func(ctx context.Context) error {
			current, _, err := source.read(ctx)
			if err != nil || current != binding {
				return ports.ErrRejected
			}
			return nil
		}
		probe = func(ctx context.Context, client *http.Client) error {
			return tvdb.NewClientWithPIN(credentials.APIKey, credentials.PIN, client).ProbeDiagnosticAuthentication(ctx)
		}
	default:
		return result
	}
	if s.Validate(ctx) != nil {
		result.Category = "configuration_changed"
		return result
	}
	return runEnrichmentDiagnostic(ctx, target, p.httpClient, p.budget, probe, s.Validate)
}
func diagnosticOpenFailure(result ports.MetadataDiagnosticResult, err error) ports.MetadataDiagnosticResult {
	if errors.Is(err, ports.ErrNotFound) {
		result.Category = "not_configured"
	}
	return result
}

type enrichmentDiagnosticTransport struct {
	base      http.RoundTripper
	budget    ports.MetadataRequestBudget
	responded bool
	closeErr  error
}

func (t *enrichmentDiagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// Native clients own fixed origins; guard again before forwarding credentials.
	fanart := r.URL.Host == "webservice.fanart.tv" && r.Method == "GET" && r.URL.Path == "/v3/movies/550"
	tvdb := r.URL.Host == "api4.thetvdb.com" && r.Method == "POST" && r.URL.Path == "/v4/login"
	if r.URL.Scheme != "https" || r.URL.User != nil || (!fanart && !tvdb) {
		return nil, ports.ErrRejected
	}
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	t.responded = true
	if response.StatusCode == 429 {
		seconds, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 64)
		if err != nil || seconds <= 0 {
			seconds = 5
		}
		t.budget.RecordRateLimit(time.Duration(min(seconds, 300)) * time.Second)
	}
	response.Body = enrichmentDiagnosticBody{ReadCloser: response.Body, transport: t}
	return response, nil
}

type enrichmentDiagnosticBody struct {
	io.ReadCloser
	transport *enrichmentDiagnosticTransport
}

func (b enrichmentDiagnosticBody) Close() error {
	err := b.ReadCloser.Close()
	b.transport.closeErr = err
	return err
}
func runEnrichmentDiagnostic(parent context.Context, target ports.MetadataDiagnosticTarget, supplied *http.Client, budget ports.MetadataRequestBudget, probe func(context.Context, *http.Client) error, validate func(context.Context) error) ports.MetadataDiagnosticResult {
	result := ports.MetadataDiagnosticResult{Target: target, Category: "unavailable"}
	if budget == nil {
		return result
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	release, err := budget.Acquire(ctx)
	if err != nil {
		result.Category = diagnosticCategory(ctx, err)
		return result
	}
	if release == nil {
		return result
	}
	defer release()
	// Recheck after admission: configuration may change while waiting for budget.
	if validate(ctx) != nil {
		result.Category = "configuration_changed"
		return result
	}
	client := http.Client{Timeout: 10 * time.Second}
	if supplied != nil {
		client = *supplied
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport := &enrichmentDiagnosticTransport{base: base, budget: budget}
	client.Transport = transport
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	err = probe(ctx, &client)
	if transport.closeErr != nil {
		err = ports.ErrUnavailable
	}
	result.HostResponded = transport.responded
	result.Category = diagnosticCategory(ctx, err)
	if validate(parent) != nil {
		return ports.MetadataDiagnosticResult{Target: target, Category: "configuration_changed"}
	}
	return result
}
func diagnosticCategory(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, ports.ErrTimeout):
		return "timeout"
	case err == nil:
		return "available"
	case errors.Is(err, ports.ErrAuthRequired):
		return "auth_required"
	case errors.Is(err, ports.ErrBadResponse), errors.Is(err, ports.ErrRejected):
		return "bad_response"
	case errors.Is(err, ports.ErrNotFound):
		return "host_response"
	}
	if _, ok := ports.IsRateLimited(err); ok {
		return "rate_limited"
	}
	return "unavailable"
}
