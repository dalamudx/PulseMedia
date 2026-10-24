package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"

	tvdbadapter "github.com/dalamudx/PulseMedia/internal/adapter/metadata/tvdb"
	tvdbconfig "github.com/dalamudx/PulseMedia/internal/domain/tvdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatafallback"
)

type tvdbSource struct {
	settings   artworkSettings
	httpClient *http.Client
	identity   ports.TVArtworkIdentityResolver
	resolver   ports.TVDBEpisodeResolver
}
type tvdbBinding struct {
	credentialVersion, fallbackVersion int64
	digest                             [32]byte
}

func (s tvdbSource) read(ctx context.Context) (tvdbBinding, tvdbconfig.Credentials, error) {
	var binding tvdbBinding
	var credentials tvdbconfig.Credentials
	if err := ctx.Err(); err != nil {
		return binding, credentials, err
	}
	if s.settings == nil {
		return binding, credentials, ports.ErrNotFound
	}
	sw, err := s.settings.GetSetting(ctx, tvdbconfig.FallbackSetting)
	if err != nil {
		return binding, credentials, ports.ErrUnavailable
	}
	if sw == nil {
		return binding, credentials, ports.ErrNotFound
	}
	raw, err := json.Marshal(sw.Value)
	if err != nil {
		return binding, credentials, ports.ErrUnavailable
	}
	enabled, err := tvdbconfig.DecodeEnabled(raw)
	if err != nil || sw.Key != tvdbconfig.FallbackSetting || sw.Version <= 0 {
		return binding, credentials, ports.ErrUnavailable
	}
	if !enabled {
		return binding, credentials, ports.ErrNotFound
	}
	item, err := s.settings.GetSetting(ctx, tvdbconfig.CredentialsSetting)
	if err != nil || item == nil || item.Key != tvdbconfig.CredentialsSetting || item.Version <= 0 {
		return binding, credentials, ports.ErrUnavailable
	}
	secret, err := s.settings.GetSecret(ctx, tvdbconfig.CredentialsSetting)
	if err != nil {
		return binding, credentials, ports.ErrUnavailable
	}
	credentials, err = tvdbconfig.DecodeCredentials([]byte(secret))
	if err != nil {
		return binding, tvdbconfig.Credentials{}, ports.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return binding, tvdbconfig.Credentials{}, err
	}
	binding = tvdbBinding{credentialVersion: item.Version, fallbackVersion: sw.Version, digest: sha256.Sum256([]byte(secret))}
	return binding, credentials, nil
}
func (s tvdbSource) ReadSeasonSupplement(ctx context.Context, id int64, season int, primary *ports.TVSeasonMetadata) (*ports.TVDBSeasonSupplement, error) {
	binding, credentials, err := s.read(ctx)
	if errors.Is(err, ports.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	current, _, err := s.read(ctx)
	if err != nil || current != binding {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	source := tvdbadapter.NewClientWithPIN(credentials.APIKey, credentials.PIN, s.httpClient)
	result, err := metadatafallback.New(s.identity, source, s.resolver).ReadSeasonSupplement(ctx, id, season, primary)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	current, _, err = s.read(ctx)
	if err != nil || current != binding {
		return nil, ports.ErrSubscriptionBindingsConflict
	}
	if result == nil {
		return nil, ports.ErrBadResponse
	}
	result.CredentialVersion = binding.credentialVersion
	result.FallbackVersion = binding.fallbackVersion
	return result, nil
}
func (p *dynamicTMDBProvider) ResolveTVDBEpisode(ctx context.Context, tmdbID, tvdbID int64) (*ports.TVDBEpisodeResolution, error) {
	resolver, ok := p.getClient(ctx).(ports.TVDBEpisodeResolver)
	if !ok {
		return nil, ports.ErrUnavailable
	}
	return resolver.ResolveTVDBEpisode(ctx, tmdbID, tvdbID)
}
