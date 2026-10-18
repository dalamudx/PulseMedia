package app

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Opt-in: transfers only self-generated private torrents between the two owned
// M4 containers, without public trackers, DHT, or external peers.
func TestM4RealDownloaders(t *testing.T) {
	root := os.Getenv("PULSE_M4_SERVICES_DIR")
	if root == "" {
		t.Skip("explicit owned Podman M4 service directory required")
	}
	clean, err := filepath.EvalSymlinks(root)
	if err != nil || clean != root || filepath.Dir(root) != "/data/temp" || !strings.HasPrefix(filepath.Base(root), "pulse-m4-services.") {
		t.Fatal("explicit canonical M4 service directory required")
	}
	qb, err := qbittorrent.NewClient("http://127.0.0.1:18085", "", "", &http.Client{Transport: m4HTTPStatusTrace{t: t}})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := transmission.NewClient("http://127.0.0.1:19091", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	clients := []struct {
		name   string
		client ports.DownloadClient
	}{{"qbittorrent", qb}, {"transmission", tr}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var trIP string
	for _, c := range clients {
		label, e := exec.CommandContext(ctx, "podman", "inspect", "--format", "{{index .Config.Labels \"io.pulsemedia.state\"}}", "pulsemedia-m4-"+c.name).Output()
		if e != nil || strings.TrimSpace(string(label)) != root {
			t.Fatal("refusing a container outside the explicit service root")
		}
		if e := c.client.Test(ctx); e != nil {
			t.Fatalf("%s service: %v", c.name, e)
		}
		if c.name == "transmission" {
			data, e := exec.CommandContext(ctx, "podman", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", "pulsemedia-m4-transmission").Output()
			trIP = strings.TrimSpace(string(data))
			if e != nil || net.ParseIP(trIP) == nil || !net.ParseIP(trIP).IsPrivate() {
				t.Fatal("owned Transmission private peer address unavailable")
			}
		}
	}
	for targetIndex, target := range clients {
		t.Run(target.name, func(t *testing.T) {
			stage, stop := context.WithTimeout(ctx, 90*time.Second)
			defer stop()
			seed := clients[1-targetIndex]
			probe, e := os.MkdirTemp(filepath.Join(root, "downloads"), "m4-probe.")
			if e != nil {
				t.Fatal(e)
			}
			name := "PulseMedia-" + filepath.Base(probe) + ".bin"
			payload := bytes.Repeat([]byte(name), 500)
			if len(payload) > 65536 {
				t.Fatal("single-piece fixture exceeded")
			}
			for _, folder := range []string{probe, filepath.Join(probe, "seed"), filepath.Join(probe, "receive")} {
				if e := os.MkdirAll(folder, 0755); e != nil {
					t.Fatal(e)
				}
				if e := os.Chmod(folder, 0755); e != nil {
					t.Fatal(e)
				}
				if os.Geteuid() == 0 {
					if e := os.Chown(folder, 1000, 1000); e != nil {
						t.Fatal(e)
					}
				}
			}
			seedFile := filepath.Join(probe, "seed", name)
			if e := os.WriteFile(seedFile, payload, 0644); e != nil {
				t.Fatal(e)
			}
			if os.Geteuid() == 0 {
				if e := os.Chown(seedFile, 1000, 1000); e != nil {
					t.Fatal(e)
				}
			}
			piece := sha1.Sum(payload)
			info := []byte(fmt.Sprintf("d6:lengthi%de4:name%d:%s12:piece lengthi65536e6:pieces20:", len(payload), len(name), name))
			info = append(info, piece[:]...)
			info = append(info, []byte("7:privatei1ee")...)
			hash := sha1.Sum(info)
			identity := hex.EncodeToString(hash[:])
			const tracker = "http://127.0.0.1:1/announce"
			raw := []byte(fmt.Sprintf("d8:announce%d:%s4:info", len(tracker), tracker))
			raw = append(raw, info...)
			raw = append(raw, 'e')
			t.Cleanup(func() {
				cleanup, closeCtx := context.WithTimeout(context.Background(), 20*time.Second)
				defer closeCtx()
				for _, c := range clients {
					_, observed := c.client.GetTorrent(cleanup, identity)
					if observed == nil {
						if e := c.client.RemoveTorrent(cleanup, identity, false); e != nil {
							t.Errorf("owned %s cleanup failed, retained %s: %v", c.name, probe, e)
							return
						}
					} else if !errors.Is(observed, ports.ErrNotFound) {
						t.Errorf("cannot observe %s cleanup, retained %s: %v", c.name, probe, observed)
						return
					}
					if !m4WaitTorrent(cleanup, func() bool { _, e := c.client.GetTorrent(cleanup, identity); return errors.Is(e, ports.ErrNotFound) }) {
						t.Errorf("owned %s removal unconfirmed, retained %s", c.name, probe)
						return
					}
				}
				if e := os.RemoveAll(probe); e != nil {
					t.Errorf("owned fixture cleanup: %v", e)
				}
			})
			base := path.Join("/downloads", filepath.Base(probe))
			if e := seed.client.AddTorrent(stage, ports.AddTorrentParam{InfoHash: identity, TorrentBytes: raw, SavePath: path.Join(base, "seed")}); e != nil {
				t.Fatalf("seed Add not retried: %v", e)
			}
			if !m4WaitTorrent(stage, func() bool { v, e := seed.client.GetTorrent(stage, identity); return e == nil && v.Progress == 1 }) {
				t.Fatal("private local seed did not verify")
			}
			tag := "pulsemedia:op:" + filepath.Base(probe)
			savePath := path.Join(base, "receive")
			if e := target.client.AddTorrent(stage, ports.AddTorrentParam{InfoHash: identity, TorrentBytes: raw, SavePath: savePath, Tags: tag}); e != nil {
				t.Fatalf("receiver Add not retried: %v", e)
			}
			if !m4WaitTorrent(stage, func() bool { _, e := qb.GetTorrent(stage, identity); return e == nil }) {
				t.Fatal("qbit side was not observed before connecting owned peer")
			}
			values := url.Values{"hashes": {identity}, "peers": {net.JoinHostPort(trIP, "51413")}}
			req, e := http.NewRequestWithContext(stage, http.MethodPost, "http://127.0.0.1:18085/api/v2/torrents/addPeers", strings.NewReader(values.Encode()))
			if e != nil {
				t.Fatal(e)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response, e := (&http.Client{Timeout: 10 * time.Second}).Do(req)
			if e != nil {
				t.Fatal("owned peer connection request failed")
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("owned peer connection HTTP %d", response.StatusCode)
			}
			var actual *ports.TorrentInfo
			if !m4WaitTorrent(stage, func() bool {
				actual, err = target.client.GetTorrent(stage, identity)
				return err == nil && actual.Progress == 1 && actual.DownloadedBytes == int64(len(payload))
			}) {
				t.Fatalf("real transfer completion unavailable: observation=%+v error=%v", actual, err)
			}
			if actual.InfoHash != identity || actual.Name != name || path.Clean(actual.SavePath) != savePath || actual.TotalBytes != int64(len(payload)) {
				t.Fatalf("real task identity mismatch: %+v", actual)
			}
			files, e := target.client.(ports.DownloadFileInventoryClient).GetTorrentFiles(stage, identity)
			if e != nil || len(files) != 1 || files[0].Name != name || files[0].Progress != 1 || files[0].SizeBytes != int64(len(payload)) {
				t.Fatalf("real file-list mismatch: %+v %v", files, e)
			}
			matches, e := target.client.ListTorrents(stage, tag)
			if e != nil || len(matches) != 1 || matches[0].InfoHash != identity || matches[0].DownloadedBytes != int64(len(payload)) || !m4HasTag(matches[0].Tags, tag) {
				t.Fatalf("real operation-tag listing mismatch: %+v %v", matches, e)
			}
			other, e := target.client.ListTorrents(stage, tag+"-absent")
			if e != nil || other == nil || len(other) != 0 {
				t.Fatalf("absent operation tag not excluded: count=%d error=%v", len(other), e)
			}
			for _, folder := range []string{"seed", "receive"} {
				got, e := os.ReadFile(filepath.Join(probe, folder, name))
				if e != nil || !bytes.Equal(got, payload) {
					t.Fatalf("%s payload differs after real transfer", folder)
				}
			}
			t.Logf("M4_REAL_DOWNLOADER_PASSED client=%s hash=%s bytes=%d peer_transfer=true complete_file_list=true exact_operation_tag=true seed_preserved=true", target.name, identity, len(payload))
		})
	}
	// Confirm that this run reached the real qBittorrent Web API version endpoint.
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:18085/api/v2/app/webapiVersion", nil)
	if e != nil {
		t.Fatal(e)
	}
	r, e := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if e != nil {
		t.Fatal("Web API version unavailable")
	}
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode != http.StatusOK {
		t.Fatal("Web API version response failed")
	}
	var major int
	var version bytes.Buffer
	if _, e := version.ReadFrom(http.MaxBytesReader(nil, r.Body, 100)); e != nil {
		t.Fatal(e)
	}
	major, _ = strconv.Atoi(strings.Split(strings.TrimSpace(version.String()), ".")[0])
	if major < 2 {
		t.Fatal("unsupported Web API version")
	}
	t.Logf("M4_WEB_API_VERSION %s", strings.TrimSpace(version.String()))
}

func m4WaitTorrent(ctx context.Context, observe func() bool) bool {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if observe() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}
func m4HasTag(tags, wanted string) bool {
	for _, tag := range strings.Split(tags, ",") {
		if strings.TrimSpace(tag) == wanted {
			return true
		}
	}
	return false
}

type m4HTTPStatusTrace struct{ t *testing.T }

func (trace m4HTTPStatusTrace) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := http.DefaultTransport.RoundTrip(request)
	if response != nil && request.URL.Path == "/api/v2/torrents/add" {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(body))
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(body, &fields)
		keys := make([]string, 0, len(fields))
		for key := range fields {
			if len(key) < 40 {
				keys = append(keys, key)
			}
		}
		trace.t.Logf("M4_REAL_QBIT_ADD_HTTP status=%d bytes=%d ok_marker=%t fails_marker=%t json_keys=%v read_failed=%t", response.StatusCode, len(body), strings.TrimSpace(string(body)) == "Ok.", strings.TrimSpace(string(body)) == "Fails.", keys, readErr != nil)
	}
	return response, err
}
