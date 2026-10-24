package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type m11TVArtworkTransport func(*http.Request) (*http.Response, error)

func (f m11TVArtworkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestM11ArtworkTVNativeHTTPPG(t *testing.T) {
	for _, scenario := range []string{"mapped", "unmapped", "mismatched"} {
		t.Run(scenario, func(t *testing.T) { m11ArtworkTV(t, scenario) })
	}
}

func m11ArtworkTV(t *testing.T, scenario string) {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	picture.SetNRGBA(0, 0, color.NRGBA{G: 100, A: 128})
	var pngBody, jpgBody bytes.Buffer
	if err := png.Encode(&pngBody, picture); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpgBody, picture, nil); err != nil {
		t.Fatal(err)
	}
	entered, released := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(released) })
	var identities, apiCalls, images atomic.Int32
	native := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("non-read artwork request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case r.URL.Path == "/3/tv/550/external_ids":
			if identities.Add(1) != 1 {
				t.Error("unexpected external identity replay")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("api_key") == "" {
				t.Error("missing metadata credential")
			}
			close(entered)
			select {
			case <-released:
			case <-r.Context().Done():
				return
			}
			body := map[string]any{"id": 550, "tvdb_id": 81189}
			if scenario == "unmapped" {
				body["tvdb_id"] = nil
			}
			if scenario == "mismatched" {
				body["id"] = 551
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(body); err != nil {
				t.Error(err)
			}
		case r.URL.Path == "/v3/tv/81189":
			apiCalls.Add(1)
			if r.URL.Query().Get("api_key") != "owned-tv-fanart-key" {
				t.Error("wrong artwork credential")
			}
			body := map[string]any{"thetvdb_id": "81189"}
			for field, name := range map[string]string{"hdtvlogo": "logo.png", "hdclearart": "clearart.png", "tvbanner": "banner.jpg"} {
				body[field] = []map[string]string{{"id": "1", "lang": "zh", "url": "https://assets.fanart.tv/fanart/tv/81189/" + field + "/" + name}}
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(body); err != nil {
				t.Error(err)
			}
		case strings.HasPrefix(r.URL.Path, "/fanart/tv/81189/"):
			images.Add(1)
			if r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("asset credential leak")
			}
			if strings.HasSuffix(r.URL.Path, ".jpg") {
				w.Header().Set("Content-Type", "image/jpeg")
				_, _ = w.Write(jpgBody.Bytes())
			} else {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(pngBody.Bytes())
			}
		default:
			t.Error("unexpected identity/artwork path", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(native.Close)
	h := newM5AutomaticTVAcceptance(t, false)
	t.Cleanup(release)
	target, err := url.Parse(native.URL)
	if err != nil {
		t.Fatal(err)
	}
	h.metadata.mu.Lock()
	h.metadata.artworkTransport = m11TVArtworkTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" {
			return nil, errors.New("insecure artwork fixture request")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		clone.URL, clone.Host = &u, target.Host
		return native.Client().Transport.RoundTrip(clone)
	})
	h.metadata.mu.Unlock()
	admin := m5CreationLogin(t, h.f, "creation-admin")
	for key, value := range map[string]any{"fanart.api_key": map[string]any{"api_key": "owned-tv-fanart-key"}, "fanart.artwork": map[string]any{"enabled": true}} {
		reply := h.f.request(t, http.MethodPut, "/api/v1/settings/"+key, "m11-tv-"+key, map[string]any{"expected_version": 0, "value": value}, admin)
		m5IndexerStatus(t, reply, http.StatusOK)
		if bytes.Contains(reply.body, []byte("owned-tv-fanart-key")) {
			t.Fatal("configuration response leaked credential")
		}
		if reply.header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("configuration response was cacheable")
		}
	}
	if identities.Load()+apiCalls.Load()+images.Load() != 0 {
		t.Fatal("configuration save performed network I/O")
	}
	receipt := h.create(t)
	op := h.awaitAdd(t)
	h.complete.Store(true)
	h.unblockAdd()
	h.awaitImported(t, op, 100*time.Second)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("TV external identity read did not start")
	}
	before := m11TVArtworkProtected(t, h)
	release()
	h.assertImported(t, op, receipt)
	var raw []byte
	var state, relative string
	if err = h.f.app.DBPool().QueryRow(t.Context(), `SELECT state,result FROM job WHERE kind='media_sidecar'`).Scan(&state, &raw); err != nil || state != "succeeded" {
		t.Fatal("TV sidecar completion", state, err)
	}
	if err = h.f.app.DBPool().QueryRow(t.Context(), `SELECT f.rel_path FROM library_file f JOIN library_file_unit u ON u.file_id=f.id WHERE u.unit_id=$1 AND f.root_id=$2 AND f.retired_at IS NULL`, h.unit, h.root).Scan(&relative); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(filepath.Dir(relative))
	cap, err := rootfs.NewFilesystem().OpenRoot(t.Context(), h.rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cap.Close()
	var result ports.MediaSidecarCompletion
	if json.Unmarshal(raw, &result) != nil {
		t.Fatal("invalid TV sidecar receipt")
	}
	expected := map[string]bool{"tvshow.nfo": true}
	if scenario == "mapped" {
		expected["logo.png"], expected["clearart.png"], expected["banner.jpg"] = true, true, true
	}
	if len(result.Artifacts) != len(expected) {
		t.Fatal("unexpected TV artifact count", len(result.Artifacts))
	}
	for _, artifact := range result.Artifacts {
		if !expected[artifact.Name] || artifact.Result.Disposition != ports.SidecarCreated || !artifact.Result.Published {
			t.Fatal("unexpected TV artifact receipt", artifact.Name)
		}
		delete(expected, artifact.Name)
		actual, e := cap.Inspect(t.Context(), filepath.Join(directory, artifact.Name))
		if e != nil || actual != artifact.Result.Identity {
			t.Fatal("TV artwork inode receipt mismatch", e)
		}
		if artifact.Name == "tvshow.nfo" {
			continue
		}
		data, e := os.ReadFile(filepath.Join(h.rootPath, directory, artifact.Name))
		if e != nil {
			t.Fatal(e)
		}
		decoded, format, e := image.Decode(bytes.NewReader(data))
		if e != nil || decoded.Bounds() != picture.Bounds() {
			t.Fatal("invalid TV image", e)
		}
		if strings.HasSuffix(artifact.Name, ".png") {
			_, _, _, alpha := decoded.At(0, 0).RGBA()
			if format != "png" || alpha != 128*257 {
				t.Fatal("TV transparency lost")
			}
		} else if format != "jpeg" {
			t.Fatal("invalid TV banner")
		}
	}
	absent := []string{"disc.png"}
	if scenario != "mapped" {
		absent = append(absent, "logo.png", "clearart.png", "banner.jpg")
	}
	for _, name := range absent {
		if _, e := os.Lstat(filepath.Join(h.rootPath, directory, name)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("unsupported or unbound artwork published", name, e)
		}
	}
	wantAPI, wantImages := int32(0), int32(0)
	if scenario == "mapped" {
		wantAPI, wantImages = 1, 3
	}
	if identities.Load() != 1 || apiCalls.Load() != wantAPI || images.Load() != wantImages {
		t.Fatal("TV native request count", identities.Load(), apiCalls.Load(), images.Load())
	}
	if before != m11TVArtworkProtected(t, h) {
		t.Fatal("artwork changed TV ingestion graph")
	}
	t.Logf("M11_ARTWORK_TV_APP_PASS scenario=%s TMDB=550 TVDB=81189 identity=EXPLICIT Add=ONCE import=REAL_HARDLINK original_key=READ_ONLY files=RECEIPT_MATCH protected=UNCHANGED", scenario)
}

func m11TVArtworkProtected(t *testing.T, h *m5AutomaticTVAcceptance) string {
	t.Helper()
	var snapshot string
	if err := h.f.app.DBPool().QueryRow(context.Background(), `SELECT jsonb_build_object('facts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM unit_fact x),'maps',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.file_id,x.unit_id) FROM library_file_unit x),'requirements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_requirement x),'tasks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM download_task x),'plans',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.id) FROM transfer_plan x))::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
