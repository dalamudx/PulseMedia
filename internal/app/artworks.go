package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/fanart"
	"github.com/dalamudx/PulseMedia/internal/domain/artwork"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type artworkSettings interface {
	GetSetting(context.Context, string) (*ports.SettingItem, error)
	GetSecret(context.Context, string) (string, error)
}

type artworkSource struct {
	settings   artworkSettings
	httpClient *http.Client
}
type artworkBinding struct {
	switchVersion, keyVersion int64
	keyDigest                 [32]byte
}
type artworkSession struct {
	source  artworkSource
	binding artworkBinding
	client  ports.MediaArtworkProvider
}

func (s artworkSource) read(ctx context.Context) (artworkBinding, string, error) {
	var zero artworkBinding
	if s.settings == nil {
		return zero, "", ports.ErrNotFound
	}
	sw, err := s.settings.GetSetting(ctx, artwork.SwitchSetting)
	if err != nil {
		return zero, "", ports.ErrUnavailable
	}
	if sw == nil {
		return zero, "", ports.ErrNotFound
	}
	raw, err := json.Marshal(sw.Value)
	if err != nil {
		return zero, "", ports.ErrUnavailable
	}
	enabled, err := artwork.DecodeSwitch(raw)
	if err != nil || sw.Key != artwork.SwitchSetting || sw.Version <= 0 {
		return zero, "", ports.ErrUnavailable
	}
	if !enabled {
		return zero, "", ports.ErrNotFound
	}
	key, err := s.settings.GetSetting(ctx, artwork.APIKeySetting)
	if err != nil {
		return zero, "", ports.ErrUnavailable
	}
	if key == nil {
		return zero, "", ports.ErrNotFound
	}
	if key.Key != artwork.APIKeySetting || key.Version <= 0 {
		return zero, "", ports.ErrUnavailable
	}
	secret, err := s.settings.GetSecret(ctx, artwork.APIKeySetting)
	if err != nil || !artwork.ValidAPIKey(secret) {
		return zero, "", ports.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return zero, "", err
	}
	return artworkBinding{switchVersion: sw.Version, keyVersion: key.Version, keyDigest: sha256.Sum256([]byte(secret))}, secret, nil
}

func (s artworkSource) Open(ctx context.Context) (ports.MediaArtworkSession, error) {
	binding, secret, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	session := &artworkSession{source: s, binding: binding, client: fanart.NewClient(secret, s.httpClient)}
	if err := session.Validate(ctx); err != nil {
		return nil, err
	}
	return session, nil
}
func (s *artworkSession) Validate(ctx context.Context) error {
	binding, _, err := s.source.read(ctx)
	if err != nil {
		return err
	}
	if binding != s.binding {
		return ports.ErrRejected
	}
	return nil
}
func (s *artworkSession) GetArtworks(ctx context.Context, id ports.ArtworkIdentity) (ports.MediaArtworks, error) {
	if err := s.Validate(ctx); err != nil {
		return ports.MediaArtworks{}, err
	}
	result, err := s.client.GetArtworks(ctx, id)
	if err != nil {
		return ports.MediaArtworks{}, err
	}
	if err := s.Validate(ctx); err != nil {
		return ports.MediaArtworks{}, err
	}
	return result, nil
}
func (s *artworkSession) FetchArtwork(ctx context.Context, reference string) ([]byte, error) {
	if err := s.Validate(ctx); err != nil {
		return nil, err
	}
	result, err := s.client.FetchArtwork(ctx, reference)
	if err != nil {
		return nil, err
	}
	if err := s.Validate(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (p *dynamicTMDBProvider) ResolveTVArtworkIdentity(ctx context.Context, id int64) (ports.TVArtworkIdentity, error) {
	resolver, ok := p.getClient(ctx).(ports.TVArtworkIdentityResolver)
	if !ok {
		return ports.TVArtworkIdentity{}, ports.ErrUnavailable
	}
	return resolver.ResolveTVArtworkIdentity(ctx, id)
}
