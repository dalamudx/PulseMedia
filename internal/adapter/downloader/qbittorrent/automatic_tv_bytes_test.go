package qbittorrent

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestAutomaticTVByteSenderNativeMarker(t *testing.T) {
	p := ports.AddTorrentParam{
		TorrentBytes: []byte("original-inspected-metainfo\x00\xff"), InfoHash: strings.Repeat("a", 40),
		SavePath: "/downloads/TV", Tags: "pulsemedia,pulsemedia:op:12345678-1234-4234-9234-123456789abc",
	}
	good := fmt.Sprintf(`{"added_torrent_ids":[%q],"failure_count":0,"pending_count":0,"success_count":1}`, p.InfoHash)
	for name, ack := range map[string]string{
		"affirmative":        good,
		"legacy affirmative": "Ok.",
		"wrong hash":         strings.Replace(good, p.InfoHash, strings.Repeat("b", 40), 1),
		"no success":         strings.Replace(good, `"success_count":1`, `"success_count":0`, 1),
		"multiple successes": strings.Replace(good, `"success_count":1`, `"success_count":2`, 1),
		"pending":            strings.Replace(good, `"pending_count":0`, `"pending_count":1`, 1),
		"failed":             strings.Replace(good, `"failure_count":0`, `"failure_count":1`, 1),
		"missing hash":       `{"failure_count":0,"pending_count":0,"success_count":1}`,
		"duplicate field":    strings.Replace(good, `"success_count":1`, `"success_count":0,"success_count":1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var count atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/api/v2/torrents/add" {
					t.Error("unexpected request")
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = r.MultipartForm.RemoveAll() }()
				if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.Value) != 3 || r.FormValue("savepath") != p.SavePath || r.FormValue("tags") != p.Tags || r.FormValue("paused") != "false" || r.FormValue("urls") != "" {
					t.Error("must preserve path/tags and send only the original torrent file")
				}
				file, _, err := r.FormFile("torrents")
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = file.Close() }()
				raw, err := io.ReadAll(file)
				if err != nil || !bytes.Equal(raw, p.TorrentBytes) {
					t.Error("original bytes changed")
				}
				_, _ = io.WriteString(w, ack)
			}))
			defer s.Close()
			c, err := NewClient(s.URL, "", "", s.Client())
			if err != nil {
				t.Fatal(err)
			}
			c.loggedIn = true
			err = c.AutomaticTVByteSender().AddTorrent(t.Context(), p)
			wantSuccess := name == "affirmative" || name == "legacy affirmative"
			if (err == nil) != wantSuccess || count.Load() != 1 {
				t.Fatalf("requests=%d err=%v", count.Load(), err)
			}
		})
	}
}

func TestAutomaticTVByteSenderRejectsNonByteInput(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer s.Close()
	c, err := NewClient(s.URL, "", "", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	base := ports.AddTorrentParam{InfoHash: strings.Repeat("a", 40), SavePath: "/downloads/TV", Tags: "pulsemedia,pulsemedia:op:12345678-1234-4234-9234-123456789abc"}
	for _, p := range []ports.AddTorrentParam{
		base,
		{TorrentBytes: []byte("original"), DownloadURL: "https://private.example/passkey", InfoHash: base.InfoHash, SavePath: base.SavePath, Tags: base.Tags},
		{TorrentBytes: make([]byte, ports.MaxTorrentMetainfoBytes+1), InfoHash: base.InfoHash, SavePath: base.SavePath, Tags: base.Tags},
	} {
		if err := c.AutomaticTVByteSender().AddTorrent(t.Context(), p); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatalf("invalid byte input: %v", err)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid inputs sent %d requests", requests.Load())
	}
}

func TestAutomaticTVByteSenderHTTP2DoesNotReplayReceivedAdd(t *testing.T) {
	var received atomic.Int32
	peerErrors := make(chan error, 1)
	s := httptest.NewUnstartedServer(nil)
	s.EnableHTTP2 = true
	s.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
		"h2": func(_ *http.Server, conn *tls.Conn, _ http.Handler) {
			if err := serveCompleteTVResetPeer(conn, &received); err != nil {
				select {
				case peerErrors <- err:
				default:
				}
			}
		},
	}
	s.StartTLS()
	defer func() {
		s.Client().CloseIdleConnections()
		s.CloseClientConnections()
		s.Close()
		select {
		case err := <-peerErrors:
			t.Errorf("HTTP/2 peer: %v", err)
		default:
		}
	}()
	c, err := NewClient(s.URL, "", "", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	c.loggedIn = true
	err = c.AutomaticTVByteSender().AddTorrent(t.Context(), ports.AddTorrentParam{
		TorrentBytes: []byte("original-inspected-metainfo"), InfoHash: strings.Repeat("a", 40),
		SavePath: "/downloads/TV", Tags: "pulsemedia,pulsemedia:op:12345678-1234-4234-9234-123456789abc",
	})
	if !errors.Is(err, ports.ErrUnavailable) || received.Load() != 1 {
		t.Fatalf("received Add streams=%d err=%v", received.Load(), err)
	}
}
