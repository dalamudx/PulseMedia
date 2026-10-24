package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/artwork"
	tvdbconfig "github.com/dalamudx/PulseMedia/internal/domain/tvdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type tvdbConfigFixture struct{ *artworkConfigFixture }

func (f tvdbConfigFixture) GetSetting(ctx context.Context, key string) (*ports.SettingItem, error) {
	translated := artwork.APIKeySetting
	if key == tvdbconfig.FallbackSetting {
		translated = artwork.SwitchSetting
	}
	item, err := f.artworkConfigFixture.GetSetting(ctx, translated)
	if item != nil {
		item.Key = key
	}
	return item, err
}
func tvdbSettingsFixture() tvdbConfigFixture {
	return tvdbConfigFixture{&artworkConfigFixture{enabled: true, switchVersion: 3, keyVersion: 2, secret: `{"api_key":"owned-api-key","pin":"owned-pin"}`}}
}

type tvdbIdentityFixture struct{ calls atomic.Int32 }

func (f *tvdbIdentityFixture) ResolveTVArtworkIdentity(_ context.Context, id int64) (ports.TVArtworkIdentity, error) {
	f.calls.Add(1)
	return ports.TVArtworkIdentity{TMDBID: id, TVDBID: 81189}, nil
}

type tvdbResolutionFixture struct{ calls atomic.Int32 }

func (f *tvdbResolutionFixture) ResolveTVDBEpisode(_ context.Context, id, sourceID int64) (*ports.TVDBEpisodeResolution, error) {
	f.calls.Add(1)
	return &ports.TVDBEpisodeResolution{TMDBSeriesID: id, TVDBEpisodeID: sourceID, SeasonNumber: 1, Episode: ports.TVEpisodeMetadata{TMDBEpisodeID: 707, EpisodeNumber: 7}}, nil
}
func tvdbSessionHTTP(t *testing.T, afterRead func()) (*http.Client, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v4/login" {
			var body map[string]string
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" || json.NewDecoder(r.Body).Decode(&body) != nil || body["apikey"] != "owned-api-key" || body["pin"] != "owned-pin" || len(body) != 2 {
				t.Error("login lost paired credentials or leaked URL authentication")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"status":"success","data":{"token":"owned-bearer"}}`)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v4/series/81189/episodes/official" || r.URL.Query().Get("season") != "1" || r.URL.Query().Get("page") != "0" || r.Header.Get("Authorization") != "Bearer owned-bearer" {
			t.Error("source identity, numbering or token changed")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if afterRead != nil {
			afterRead()
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"series":{"id":81189},"episodes":[{"id":9007,"seriesId":81189,"seasonNumber":1,"number":7,"aired":"","absoluteNumber":0}]},"links":{"next":null,"total_items":1}}`)
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	base := server.Client().Transport
	return &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api4.thetvdb.com" || r.URL.Scheme != "https" || r.URL.User != nil {
			return nil, errors.New("unexpected owned source destination")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Host, u.Scheme = target.Host, target.Scheme
		clone.URL = &u
		return base.RoundTrip(clone)
	})}, calls
}
func TestTVDBSourceNativeSnapshotAndLateConfiguration(t *testing.T) {
	for _, scenario := range []string{"valid", "disable", "switch_version", "key_version", "key_without_version", "storage"} {
		t.Run(scenario, func(t *testing.T) {
			settings := tvdbSettingsFixture()
			identity, resolver := &tvdbIdentityFixture{}, &tvdbResolutionFixture{}
			client, calls := tvdbSessionHTTP(t, func() {
				if scenario == "key_without_version" {
					settings.mu.Lock()
					settings.secret = `{"api_key":"second-owned-key","pin":"owned-pin"}`
					settings.mu.Unlock()
				} else if scenario != "valid" {
					settings.change(scenario)
				}
			})
			source := tvdbSource{settings: settings, httpClient: client, identity: identity, resolver: resolver}
			result, err := source.ReadSeasonSupplement(t.Context(), 1396, 1, nil)
			if scenario == "valid" {
				if err != nil || result == nil || result.TMDBSeriesID != 1396 || result.TVDBSeriesID != 81189 || result.CredentialVersion != 2 || result.FallbackVersion != 3 || len(result.Added) != 1 || result.Metadata.Complete || result.Metadata.Episodes[0].AirDate != nil {
					t.Fatal("native source lost identity/version/unknown date", err)
				}
			} else if result != nil || !errors.Is(err, ports.ErrSubscriptionBindingsConflict) {
				t.Fatal("late configuration accepted source facts", err)
			}
			if calls.Load() != 2 || identity.calls.Load() != 1 || resolver.calls.Load() != 1 {
				t.Fatal("native source repeated read or login", calls.Load(), identity.calls.Load(), resolver.calls.Load())
			}
		})
	}
}
func TestTVDBSourceDisabledAndInvalidConfigurationBeforeNetwork(t *testing.T) {
	for _, scenario := range []string{"disabled", "storage", "invalid_credentials"} {
		t.Run(scenario, func(t *testing.T) {
			settings := tvdbSettingsFixture()
			identity, resolver := &tvdbIdentityFixture{}, &tvdbResolutionFixture{}
			switch scenario {
			case "disabled":
				settings.enabled = false
			case "storage":
				settings.fail = true
			case "invalid_credentials":
				settings.secret = `{"api_key":""}`
			}
			client, calls := tvdbSessionHTTP(t, nil)
			result, err := (tvdbSource{settings: settings, httpClient: client, identity: identity, resolver: resolver}).ReadSeasonSupplement(t.Context(), 1396, 1, nil)
			if result != nil || (scenario == "disabled" && err != nil) || (scenario != "disabled" && !errors.Is(err, ports.ErrUnavailable)) {
				t.Fatal("configuration failure contract", err)
			}
			if calls.Load()+identity.calls.Load()+resolver.calls.Load() != 0 {
				t.Fatal("disabled or invalid source reached network")
			}
		})
	}
}
