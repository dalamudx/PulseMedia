package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/artwork"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type artworkConfigFixture struct {
	mu                        sync.Mutex
	enabled                   bool
	switchVersion, keyVersion int64
	secret                    string
	fail                      bool
}

func (f *artworkConfigFixture) GetSetting(_ context.Context, key string) (*ports.SettingItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, ports.ErrUnavailable
	}
	if key == artwork.SwitchSetting {
		return &ports.SettingItem{Key: key, Version: f.switchVersion, Value: map[string]any{"enabled": f.enabled}}, nil
	}
	return &ports.SettingItem{Key: key, Version: f.keyVersion, Value: map[string]any{"configured": true}}, nil
}
func (f *artworkConfigFixture) GetSecret(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", ports.ErrUnavailable
	}
	return f.secret, nil
}
func (f *artworkConfigFixture) change(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch kind {
	case "disable":
		f.enabled = false
		f.switchVersion++
	case "switch_version":
		f.switchVersion++
	case "key_version":
		f.keyVersion++
	case "key_without_version":
		f.secret = "second-fixture-key"
	case "storage":
		f.fail = true
	}
}
func artworkSettingsFixture() *artworkConfigFixture {
	return &artworkConfigFixture{enabled: true, switchVersion: 1, keyVersion: 1, secret: "first-fixture-key"}
}

type artworkTransport func(*http.Request) (*http.Response, error)

func (f artworkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func artworkHTTPReply() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tmdb_id":"77"}`))}
}

func TestFanartSessionConfigurationBinding(t *testing.T) {
	for _, kind := range []string{"disable", "switch_version", "key_version", "key_without_version", "storage"} {
		t.Run(kind, func(t *testing.T) {
			cfg := artworkSettingsFixture()
			var reads atomic.Int32
			source := artworkSource{settings: cfg, httpClient: &http.Client{Transport: artworkTransport(func(*http.Request) (*http.Response, error) { reads.Add(1); return artworkHTTPReply(), nil })}}
			session, err := source.Open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			cfg.change(kind)
			if session.Validate(t.Context()) == nil {
				t.Fatal("configuration change accepted")
			}
			if _, err := session.GetArtworks(t.Context(), ports.ArtworkIdentity{MediaType: "movie", Provider: "tmdb", ID: 77}); err == nil {
				t.Fatal("stale query allowed")
			}
			if _, err := session.FetchArtwork(t.Context(), "unused"); err == nil {
				t.Fatal("stale fetch allowed")
			}
			if reads.Load() != 0 {
				t.Fatal("stale session reached network")
			}
		})
	}
}

func TestFanartSessionLateConfigurationChange(t *testing.T) {
	cfg := artworkSettingsFixture()
	reads := 0
	source := artworkSource{settings: cfg, httpClient: &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		reads++
		if r.URL.Host != "webservice.fanart.tv" || r.URL.Query().Get("api_key") != "first-fixture-key" {
			t.Error("request identity changed")
		}
		cfg.change("disable")
		return artworkHTTPReply(), nil
	})}}
	session, err := source.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.GetArtworks(t.Context(), ports.ArtworkIdentity{MediaType: "movie", Provider: "tmdb", ID: 77})
	if err == nil || result != (ports.MediaArtworks{}) || reads != 1 {
		t.Fatal("late response accepted")
	}
}

func TestFanartSessionIsolationAndDisabledSource(t *testing.T) {
	cfg := artworkSettingsFixture()
	keys := []string{}
	source := artworkSource{settings: cfg, httpClient: &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		keys = append(keys, r.URL.Query().Get("api_key"))
		return artworkHTTPReply(), nil
	})}}
	first, err := source.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	id := ports.ArtworkIdentity{MediaType: "movie", Provider: "tmdb", ID: 77}
	if _, err := first.GetArtworks(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	cfg.change("key_without_version")
	second, err := source.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.GetArtworks(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if first.Validate(t.Context()) == nil || len(keys) != 2 || keys[0] != "first-fixture-key" || keys[1] != "second-fixture-key" {
		t.Fatal("sessions share mutable client")
	}
	cfg.change("disable")
	cfg.mu.Lock()
	cfg.secret = ""
	cfg.mu.Unlock()
	if session, err := source.Open(t.Context()); !errors.Is(err, ports.ErrNotFound) || session != nil {
		t.Fatal("disabled source needs secret or returns session")
	}
	if len(keys) != 2 {
		t.Fatal("disabled source requested upstream")
	}
}
