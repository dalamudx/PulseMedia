package rtorrent_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/rtorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestRTorrentControlNativeLifecycle(t *testing.T) {
	_, hash := nativeMetainfo()
	var active atomic.Int32
	active.Store(1)
	var writes, reads atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := r.BasicAuth()
		if !ok || user != "control-fixture" || secret != "control-fixture-secret" || r.URL.Path != "/RPC2" {
			t.Error("original endpoint authentication changed")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		call := readCall(t, r)
		switch call.Method {
		case "d.stop", "d.start":
			writes.Add(1)
			if len(call.Params) != 1 || wireString(t, call, 0) != hash {
				t.Error("control did not target only the original canonical hash")
			}
			if call.Method == "d.stop" {
				active.Store(0)
			} else {
				active.Store(1)
			}
			_, _ = io.WriteString(w, response(0))
		case "d.multicall2":
			reads.Add(1)
			if len(call.Params) != 17 || wireString(t, call, 16) != "d.state=" {
				t.Error("state and identity must be read in one native snapshot")
			}
			row := nativeRow(hash)
			row[9] = int(active.Load())
			_, _ = io.WriteString(w, response([]any{append(row, int(active.Load()))}))
		default:
			t.Error("unexpected control RPC", call.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client, err := rtorrent.NewAuthenticatedClient(server.URL+"/RPC2", "control-fixture", "control-fixture-secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, paused := range []bool{true, false} {
		if err := client.SetTorrentPaused(t.Context(), strings.ToUpper(hash), paused); err != nil {
			t.Fatal(err)
		}
		observed, err := client.ReadTorrentPaused(t.Context(), strings.ToUpper(hash))
		if err != nil || observed == nil || observed.Paused != paused || observed.Info.InfoHash != hash || observed.Info.SavePath != nativeDirectory || observed.Info.Tags != nativeTags {
			t.Fatalf("native state/identity lost: %+v %v", observed, err)
		}
	}
	if writes.Load() != 2 || reads.Load() != 2 {
		t.Fatal("unexpected native replay", writes.Load(), reads.Load())
	}
}

func TestRTorrentControlObservationRequiresStableEvidence(t *testing.T) {
	_, hash := nativeMetainfo()
	cases := []struct {
		name   string
		change func([]any) []any
		paused bool
		want   error
	}{
		{"running", func(r []any) []any { return []any{r} }, false, nil},
		{"paused", func(r []any) []any { r[9], r[14] = 0, 0; return []any{r} }, true, nil},
		{"completed paused", func(r []any) []any { r[6], r[8], r[9], r[14] = 1024, 1, 0, 0; return []any{r} }, true, nil},
		{"hashing", func(r []any) []any { r[10] = 1; return []any{r} }, false, ports.ErrUnavailable},
		{"checking", func(r []any) []any { r[11] = 1; return []any{r} }, false, ports.ErrUnavailable},
		{"start not yet active", func(r []any) []any { r[9] = 0; return []any{r} }, false, ports.ErrUnavailable},
		{"stop not yet inactive", func(r []any) []any { r[14] = 0; return []any{r} }, false, ports.ErrUnavailable},
		{"unknown intent", func(r []any) []any { r[14] = 2; return []any{r} }, false, ports.ErrBadResponse},
		{"text intent", func(r []any) []any { r[14] = "0"; return []any{r} }, false, ports.ErrBadResponse},
		{"missing intent", func(r []any) []any { return []any{r[:14]} }, false, ports.ErrBadResponse},
		{"duplicate hash", func(r []any) []any { return []any{r, r} }, false, ports.ErrBadResponse},
		{"missing task", func([]any) []any { return []any{} }, false, ports.ErrNotFound},
		{"different task", func(r []any) []any { r[0] = strings.Repeat("b", 40); return []any{r} }, false, ports.ErrNotFound},
		{"invalid path", func(r []any) []any { r[2] = "../other"; return []any{r} }, false, ports.ErrBadResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if readCall(t, r).Method != "d.multicall2" {
					t.Error("observation performed a mutation")
				}
				row := append(nativeRow(hash), 1)
				_, _ = io.WriteString(w, response(tc.change(row)))
			}))
			defer server.Close()
			observed, err := rtorrent.NewClient(server.URL, server.Client()).ReadTorrentPaused(t.Context(), hash)
			if !errors.Is(err, tc.want) {
				t.Fatalf("unexpected error %v, want %v", err, tc.want)
			}
			if tc.want != nil && observed != nil {
				t.Fatal("uncertain response became confirmed state")
			}
			if tc.want == nil && (observed == nil || observed.Paused != tc.paused) {
				t.Fatal("wrong confirmed state", observed)
			}
			if calls.Load() != 1 {
				t.Fatal("read retried", calls.Load())
			}
		})
	}
}

func TestRTorrentControlAcknowledgementAndNoReplay(t *testing.T) {
	_, hash := nativeMetainfo()
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); _, _ = io.WriteString(w, response(0)) }))
	defer target.Close()
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"zero", http.StatusOK, response(0), nil},
		{"nonzero", http.StatusOK, response(1), ports.ErrBadResponse},
		{"text zero", http.StatusOK, response("0"), ports.ErrBadResponse},
		{"empty", http.StatusOK, "", ports.ErrBadResponse},
		{"partial", http.StatusOK, "<methodResponse>", ports.ErrBadResponse},
		{"auth", http.StatusUnauthorized, "", ports.ErrAuthRequired},
		{"unavailable", http.StatusServiceUnavailable, "", ports.ErrUnavailable},
		{"redirect", http.StatusTemporaryRedirect, "", ports.ErrBadResponse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if readCall(t, r).Method != "d.stop" {
					t.Error("unexpected mutation")
				}
				w.Header().Set("Location", target.URL)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			err := rtorrent.NewClient(server.URL, server.Client()).SetTorrentPaused(t.Context(), hash, true)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
			if calls.Load() != 1 {
				t.Fatal("mutation retried", calls.Load())
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("mutation redirected")
	}
}

type controlRoundTrip func(*http.Request) (*http.Response, error)

func (f controlRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRTorrentControlLostAckAndInvalidInput(t *testing.T) {
	_, hash := nativeMetainfo()
	var calls atomic.Int32
	client := rtorrent.NewClient("http://owned.invalid/RPC2", &http.Client{Transport: controlRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.GetBody != nil {
			t.Error("uncertain write can be replayed by transport")
		}
		return nil, io.ErrUnexpectedEOF
	})})
	if err := client.SetTorrentPaused(t.Context(), hash, false); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("lost acknowledgement retried")
	}
	for _, input := range []string{"", "all", strings.Repeat("g", 40), strings.Repeat("a", 64), hash + ",other"} {
		if err := client.SetTorrentPaused(t.Context(), input, true); !errors.Is(err, ports.ErrDownloadControlInvalid) {
			t.Fatal("unsafe hash accepted", input, err)
		}
		if observed, err := client.ReadTorrentPaused(t.Context(), input); observed != nil || !errors.Is(err, ports.ErrDownloadControlInvalid) {
			t.Fatal("unsafe read accepted", input, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := client.SetTorrentPaused(ctx, hash, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("invalid or cancelled request reached native endpoint")
	}
}
