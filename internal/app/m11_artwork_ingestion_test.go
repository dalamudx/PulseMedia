package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/artwork"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/settings"
	"github.com/google/uuid"
)

func TestM11ArtworkMovieIngestionPG(t *testing.T) {
	for _, scenario := range []string{"publish", "preserve", "disable_during_download", "network_failure"} {
		t.Run(scenario, func(t *testing.T) {
			runFullMoviePipelineConfigured(t, false, "", "", false, true, func(f *organizationFixture, _ *ports.Actor, _ uuid.UUID, a *App) func() {
				return m11ArtworkMovieProbe(t, f, a, scenario)
			})
		})
	}
}

func m11ArtworkMovieProbe(t *testing.T, f *organizationFixture, a *App, scenario string) func() {
	t.Helper()
	admin := notificationFixtureAdmin(t, f)
	if _, err := f.q.CreateMediaIdentity(t.Context(), db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: f.media, Namespace: "tmdb.movie", ExternalID: "1"}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(postgres.NewSettingsStore(f.pool, f.gen), crypto.NewSettingsCipher(outboxTestKey))
	for key, value := range map[string]any{artwork.APIKeySetting: map[string]any{"api_key": "owned-fanart-key"}, artwork.SwitchSetting: map[string]any{"enabled": true}} {
		if _, err := svc.UpdateSetting(t.Context(), admin, key, settings.UpdateSettingRequest{Value: value}, "m11-"+key); err != nil {
			t.Fatal(err)
		}
	}
	picture := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	picture.SetNRGBA(0, 0, color.NRGBA{R: 100, A: 128})
	var pngBody, jpgBody bytes.Buffer
	if err := png.Encode(&pngBody, picture); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpgBody, picture, nil); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	var apiCalls, imageCalls atomic.Int32
	native := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v3/movies/1":
			apiCalls.Add(1)
			if r.Method != http.MethodGet || r.URL.Query().Get("api_key") != "owned-fanart-key" {
				t.Error("Fanart request identity/credential mismatch")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if scenario == "network_failure" {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fields := []string{"hdmovielogo", "hdmovieclearart", "moviebanner", "moviedisc"}
			names := []string{"logo.png", "clearart.png", "banner.jpg", "disc.png"}
			body := map[string]any{"tmdb_id": "1"}
			for i, field := range fields {
				body[field] = []map[string]string{{"id": "1", "lang": "zh", "url": "https://assets.fanart.tv/fanart/movies/1/" + field + "/" + names[i]}}
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(body); err != nil {
				t.Error(err)
			}
		case strings.HasPrefix(r.URL.Path, "/fanart/movies/1/"):
			n := imageCalls.Add(1)
			if r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				t.Error("asset request exposed credential")
			}
			if n == 1 {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			if strings.HasSuffix(r.URL.Path, ".jpg") {
				w.Header().Set("Content-Type", "image/jpeg")
				_, _ = w.Write(jpgBody.Bytes())
			} else {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(pngBody.Bytes())
			}
		default:
			t.Error("unexpected native artwork request", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(native.Close)
	t.Cleanup(releaseOnce)
	endpoint, err := url.Parse(native.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := native.Client().Transport
	a.metadataHTTPClient = &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "webservice.fanart.tv" && r.URL.Host != "assets.fanart.tv" {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		cloned := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = endpoint.Scheme, endpoint.Host
		cloned.URL = &u
		cloned.Host = endpoint.Host
		return transport.RoundTrip(cloned)
	})}
	return func() {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("production artwork read did not start")
		}
		waitSeedingOrganization(t, f)
		var rel string
		if err := f.pool.QueryRow(ctx, `SELECT rel_path FROM library_file WHERE root_id=$1 AND retired_at IS NULL AND EXISTS(SELECT 1 FROM library_file_unit u WHERE u.file_id=library_file.id)`, f.root).Scan(&rel); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(rel)
		logo := filepath.Join(f.target, dir, "logo.png")
		if scenario == "preserve" {
			if err := os.WriteFile(logo, []byte("user artwork"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		before := m11ArtworkProtected(t, f)
		if scenario == "disable_during_download" {
			if _, err := svc.UpdateSetting(ctx, admin, artwork.SwitchSetting, settings.UpdateSettingRequest{ExpectedVersion: 1, Value: map[string]any{"enabled": false}}, "m11-disable"); err != nil {
				t.Fatal(err)
			}
		}
		releaseOnce()
		var raw []byte
		var state string
		waitOutboxCondition(t, ctx, func() bool {
			return f.pool.QueryRow(ctx, `SELECT state,result FROM job WHERE kind='media_sidecar' AND state IN ('succeeded','dead','failed')`).Scan(&state, &raw) == nil
		})
		if state != "succeeded" {
			t.Fatal("artwork sidecar failed", state)
		}
		expected := map[string]ports.SidecarDisposition{"movie.nfo": ports.SidecarCreated}
		success := scenario == "publish" || scenario == "preserve"
		for _, name := range []string{"logo.png", "clearart.png", "banner.jpg", "disc.png"} {
			path := filepath.Join(f.target, dir, name)
			if !success {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("disabled/failed artwork published", name, err)
				}
				continue
			}
			expected[name] = ports.SidecarCreated
			if scenario == "preserve" && name == "logo.png" {
				expected[name] = ports.SidecarPreservedRegular
				if string(m7ReadRegular(t, path, 0600)) != "user artwork" {
					t.Fatal("user artwork replaced")
				}
				continue
			}
			data := m7ReadRegular(t, path, 0644)
			decoded, format, err := image.Decode(bytes.NewReader(data))
			if err != nil || decoded.Bounds() != picture.Bounds() {
				t.Fatal("invalid saved image", name, err)
			}
			if strings.HasSuffix(name, ".png") {
				_, _, _, alpha := decoded.At(0, 0).RGBA()
				if format != "png" || alpha != 128*257 {
					t.Fatal("PNG transparency lost", name, alpha)
				}
			} else if format != "jpeg" {
				t.Fatal("banner format changed")
			}
		}
		m7AssertArtifacts(t, raw, f.target, dir, expected)
		if before != m11ArtworkProtected(t, f) {
			t.Fatal("artwork changed ingestion or download graph")
		}
		if apiCalls.Load() != 1 || success && imageCalls.Load() != 4 || scenario == "disable_during_download" && imageCalls.Load() != 1 || scenario == "network_failure" && imageCalls.Load() != 0 {
			t.Fatal("unexpected retries or downloads", apiCalls.Load(), imageCalls.Load())
		}
		beforeAPI, beforeImages := apiCalls.Load(), imageCalls.Load()
		if _, err := svc.ReadSettingReceipt(ctx, admin, artwork.APIKeySetting, "m11-"+artwork.APIKeySetting); err != nil {
			t.Fatal(err)
		}
		if apiCalls.Load() != beforeAPI || imageCalls.Load() != beforeImages {
			t.Fatal("receipt triggered network")
		}
		t.Logf("M11_ARTWORK_MOVIE_APP_PASS scenario=%s import=REAL_HARDLINK Add=ONCE artwork=REAL_TLS PNG_ALPHA=PRESERVED configured=ENCRYPTED original_receipt=READ_ONLY protected=UNCHANGED", scenario)
	}
}

func m11ArtworkProtected(t *testing.T, f *organizationFixture) string {
	t.Helper()
	var snapshot string
	if err := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_object('facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x),'maps',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.file_id,x.unit_id) FROM library_file_unit x),'requirements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x),'tasks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_task x),'plans',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM transfer_plan x))::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
