package qbittorrent_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/transmission"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestNativeDownloadersNeverDelegateHTTPSource(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	qb, err := qbittorrent.NewClient(srv.URL, "user", "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := transmission.NewClient(srv.URL, "user", "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []ports.DownloadClient{qb, tr} {
		for _, p := range []ports.AddTorrentParam{{DownloadURL: srv.URL + "/probe"}, {DownloadURL: "magnet:?xt=urn:btih:" + strings.Repeat("a", 40) + "&xs=" + srv.URL}, {TorrentBytes: []byte("x"), DownloadURL: srv.URL}} {
			if err = c.AddTorrent(context.Background(), p); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatal(err)
			}
		}
	}
	if hits.Load() != 0 {
		t.Fatal("invalid source reached downloader login/Add", hits.Load())
	}
}

func TestOrdinaryQBAcknowledgementAndNoTransportReplay(t *testing.T) {
	for _, ack := range []string{"Ok.", "Fails.", "", strings.Repeat("x", 4097)} {
		t.Run(ack[:min(len(ack), 6)], func(t *testing.T) {
			var calls atomic.Int32
			c, err := qbittorrent.NewClient("https://downloader.invalid", "u", "p", &http.Client{Transport: sourceTransport(func(r *http.Request) (*http.Response, error) {
				body := "Ok."
				if strings.HasSuffix(r.URL.Path, "/add") {
					calls.Add(1)
					if r.GetBody != nil {
						t.Error("Add remains transport replayable")
					}
					body = ack
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			err = c.AddTorrent(context.Background(), ports.AddTorrentParam{DownloadURL: "magnet:?xt=urn:btih:" + strings.Repeat("a", 40)})
			if (err == nil) != (ack == "Ok.") || calls.Load() != 1 {
				t.Fatal("incorrect acknowledgement", err, calls.Load())
			}
		})
	}
}

type sourceTransport func(*http.Request) (*http.Response, error)

func (f sourceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
