package rtorrent_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/rtorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

const nativeDirectory = "/downloads/中文 & title"
const nativeTags = "pulsemedia,pulsemedia:12345678-1234-1234-1234-123456789abc"

func nativeMetainfo() ([]byte, string) {
	info := "d6:lengthi1024e4:name8:file.mkv12:piece lengthi16384e6:pieces20:12345678901234567890e"
	hash := sha1.Sum([]byte(info))
	return []byte("d4:info" + info + "e"), hex.EncodeToString(hash[:])
}

type wireCall struct {
	XMLName xml.Name
	Method  string `xml:"methodName"`
	Params  []struct {
		String *string `xml:"value>string"`
		Binary *string `xml:"value>base64"`
	} `xml:"params>param"`
}

func readCall(t *testing.T, r *http.Request) wireCall {
	t.Helper()
	var call wireCall
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "text/xml" || xml.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&call) != nil || call.XMLName.Local != "methodCall" {
		t.Error("invalid native RPC request")
	}
	return call
}

func wireString(t *testing.T, call wireCall, index int) string {
	t.Helper()
	if index >= len(call.Params) || call.Params[index].String == nil {
		t.Error("missing string RPC argument")
		return ""
	}
	return *call.Params[index].String
}

func nativeRow(hash string) []any {
	return []any{strings.ToUpper(hash), "file.mkv", nativeDirectory, nativeDirectory + "/file.mkv", 0, 1024, 512, 2048, 0, 1, 0, 0, nativeTags, 4096}
}

func wireValue(value any) string {
	var b bytes.Buffer
	switch v := value.(type) {
	case string:
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(v))
		b.WriteString("</string>")
	case int:
		fmt.Fprintf(&b, "<i8>%d</i8>", v)
	case int64:
		fmt.Fprintf(&b, "<i8>%d</i8>", v)
	case []any:
		b.WriteString("<array><data>")
		for _, element := range v {
			b.WriteString("<value>" + wireValue(element) + "</value>")
		}
		b.WriteString("</data></array>")
	default:
		panic("unsupported owned fixture value")
	}
	return b.String()
}

func response(value any) string {
	return "<methodResponse><params><param><value>" + wireValue(value) + "</value></param></params></methodResponse>"
}

func TestRTorrentNativeLifecycleTLS(t *testing.T) {
	raw, hash := nativeMetainfo()
	var present, complete atomic.Bool
	var adds, erases atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "owned-user" || password != "owned-password" || r.URL.Path != "/RPC2" {
			t.Error("wrong endpoint or credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		call := readCall(t, r)
		value := any(0)
		switch call.Method {
		case "system.client_version":
			value = "0.9.8"
		case "load.raw_start":
			adds.Add(1)
			if len(call.Params) != 4 || wireString(t, call, 0) != "" || call.Params[1].Binary == nil {
				t.Error("load omitted native payload or commands")
				break
			}
			decoded, err := base64.StdEncoding.DecodeString(*call.Params[1].Binary)
			if err != nil || !bytes.Equal(raw, decoded) || wireString(t, call, 2) != `d.directory.set="`+nativeDirectory+`"` || wireString(t, call, 3) != `d.custom.set=pulsemedia.tags,"`+nativeTags+`"` {
				t.Error("load changed bytes/path/tag")
			}
			present.Store(true)
		case "d.multicall2":
			expected := []string{"", "main", "d.hash=", "d.name=", "d.directory=", "d.base_path=", "d.is_multi_file=", "d.size_bytes=", "d.completed_bytes=", "d.up.total=", "d.complete=", "d.is_active=", "d.hashing=", "d.is_hash_checking=", "d.custom=pulsemedia.tags", "d.down.rate="}
			if len(call.Params) != len(expected) {
				t.Error("incomplete native observation")
			} else {
				for i, arg := range expected {
					if wireString(t, call, i) != arg {
						t.Error("wrong observation field")
					}
				}
			}
			rows := []any{}
			if present.Load() {
				row := nativeRow(hash)
				if complete.Load() {
					row[6], row[8] = 1024, 1
				}
				rows = append(rows, row)
			}
			value = rows
		case "d.erase":
			erases.Add(1)
			if len(call.Params) != 1 || wireString(t, call, 0) != strings.ToUpper(hash) {
				t.Error("erase missing exact task identity")
			}
			present.Store(false)
		default:
			t.Error("unexpected RPC method")
		}
		_, _ = io.WriteString(w, response(value))
	}))
	defer server.Close()
	client, err := rtorrent.NewAuthenticatedClient(server.URL+"/RPC2", "owned-user", "owned-password", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Test(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.AddTorrent(ctx, ports.AddTorrentParam{InfoHash: hash, TorrentBytes: raw, SavePath: nativeDirectory, Tags: nativeTags}); err != nil {
		t.Fatal(err)
	}
	info, err := client.GetTorrent(ctx, hash)
	if err != nil || info.InfoHash != hash || info.TotalBytes != 1024 || info.DownloadedBytes != 512 || info.Progress != .5 || info.State != "downloading" || info.ContentPath != "" || info.SavePath != nativeDirectory || info.Tags != nativeTags || info.DownloadRateBytesPerSecond == nil || *info.DownloadRateBytesPerSecond != 4096 {
		t.Fatalf("native observation: %#v %v", info, err)
	}
	rows, err := client.ListTorrents(ctx, "pulsemedia")
	if err != nil || len(rows) != 1 {
		t.Fatal("exact tag missed")
	}
	rows, err = client.ListTorrents(ctx, "pulse")
	if err != nil || len(rows) != 0 {
		t.Fatal("substring tag accepted")
	}
	complete.Store(true)
	info, err = client.GetTorrent(ctx, strings.ToUpper(hash))
	if err != nil || info.Progress != 1 || info.State != "uploading" || info.ContentPath != nativeDirectory+"/file.mkv" || info.Ratio != 2 {
		t.Fatalf("completion: %#v %v", info, err)
	}
	if err := client.RemoveTorrent(ctx, hash, false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetTorrent(ctx, hash); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("absence: %v", err)
	}
	if adds.Load() != 1 || erases.Load() != 1 {
		t.Fatal("mutation repeated")
	}
}

func TestRTorrentLoadVariantsAndInputProtection(t *testing.T) {
	raw, hash := nativeMetainfo()
	for _, tc := range []struct {
		name, method   string
		magnet, paused bool
	}{{"raw", "load.raw_start", false, false}, {"raw_paused", "load.raw", false, true}, {"magnet", "load.start", true, false}, {"magnet_paused", "load.normal", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				call := readCall(t, r)
				if call.Method != tc.method || len(call.Params) != 2 || wireString(t, call, 0) != "" {
					t.Error("wrong load variant")
				}
				if tc.magnet && wireString(t, call, 1) != "magnet:?xt=urn:btih:"+hash {
					t.Error("magnet changed")
				}
				_, _ = io.WriteString(w, response(0))
			}))
			defer server.Close()
			client := rtorrent.NewClient(server.URL, server.Client())
			p := ports.AddTorrentParam{InfoHash: hash, TorrentBytes: raw, Paused: tc.paused}
			if tc.magnet {
				p.TorrentBytes = nil
				p.DownloadURL = "magnet:?xt=urn:btih:" + hash
			}
			if err := client.AddTorrent(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatal("load repeated")
			}
		})
	}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, response(0)) }))
	defer server.Close()
	client := rtorrent.NewClient(server.URL, server.Client())
	for _, change := range []func(*ports.AddTorrentParam){
		func(p *ports.AddTorrentParam) { p.InfoHash = strings.Repeat("b", 40) },
		func(p *ports.AddTorrentParam) { p.TorrentBytes = []byte("invalid") },
		func(p *ports.AddTorrentParam) { p.DownloadURL = "https://private.invalid/secret" },
		func(p *ports.AddTorrentParam) { p.SavePath = "/data/../escape" },
		func(p *ports.AddTorrentParam) { p.SavePath = "/data/$execute" },
		func(p *ports.AddTorrentParam) { p.SavePath = "/data/\";execute=rm" },
		func(p *ports.AddTorrentParam) { p.Tags = "tag;execute=rm" },
		func(p *ports.AddTorrentParam) { p.Tags = "tag,tag" },
	} {
		p := ports.AddTorrentParam{InfoHash: hash, TorrentBytes: raw}
		change(&p)
		if err := client.AddTorrent(context.Background(), p); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatalf("unsafe input: %v", err)
		}
	}
	if err := client.RemoveTorrent(context.Background(), hash, true); !errors.Is(err, ports.ErrRejected) {
		t.Fatal("file deletion accepted")
	}
	if err := client.RemoveTorrent(context.Background(), "bad", false); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal("invalid erase identity accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("rejected input reached network")
	}
}

func TestRTorrentObservationRejectsUnprovenFacts(t *testing.T) {
	_, hash := nativeMetainfo()
	for name, change := range map[string]func([]any) []any{
		"missing_field":  func(row []any) []any { return row[:13] },
		"wrong_type":     func(row []any) []any { row[5] = "1024"; return row },
		"negative":       func(row []any) []any { row[6] = -1; return row },
		"over_complete":  func(row []any) []any { row[6] = 1025; return row },
		"false_complete": func(row []any) []any { row[8] = 1; return row },
		"invalid_bool":   func(row []any) []any { row[9] = 2; return row },
		"unknown_hash":   func(row []any) []any { row[0] = "bad"; return row },
		"foreign_path":   func(row []any) []any { row[3] = "/elsewhere/file.mkv"; return row },
		"traversal":      func(row []any) []any { row[3] = nativeDirectory + "/../file.mkv"; return row },
		"bad_tags":       func(row []any) []any { row[12] = "pulsemedia,pulsemedia"; return row },
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, response([]any{change(nativeRow(hash))}))
			}))
			defer server.Close()
			_, err := rtorrent.NewClient(server.URL, server.Client()).GetTorrent(context.Background(), hash)
			if !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("unproven observation accepted: %v", err)
			}
		})
	}
	t.Run("duplicate_identity", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, response([]any{nativeRow(hash), nativeRow(hash)}))
		}))
		defer server.Close()
		if _, err := rtorrent.NewClient(server.URL, server.Client()).ListTorrents(context.Background(), ""); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatal("ambiguous identity accepted")
		}
	})
}

func TestRTorrentHTTPFailureBoundaries(t *testing.T) {
	_, hash := nativeMetainfo()
	fault := `<methodResponse><fault><value><struct><member><name>faultCode</name><value><int>-503</int></value></member><member><name>faultString</name><value><string>owned-secret-password</string></value></member></struct></value></fault></methodResponse>`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"authentication", http.StatusUnauthorized, "owned-secret-password", ports.ErrAuthRequired},
		{"forbidden", http.StatusForbidden, "", ports.ErrAuthRequired},
		{"unavailable", http.StatusServiceUnavailable, "", ports.ErrUnavailable},
		{"endpoint_missing", http.StatusNotFound, "", ports.ErrBadResponse},
		{"empty", http.StatusNoContent, "", ports.ErrBadResponse},
		{"fault", http.StatusOK, fault, ports.ErrBadResponse},
		{"broken", http.StatusOK, "<methodResponse>", ports.ErrBadResponse},
		{"oversize", http.StatusOK, strings.Repeat("x", (4<<20)+1), ports.ErrBadResponse},
		{"not_list", http.StatusOK, response(0), ports.ErrBadResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			_, err := rtorrent.NewClient(server.URL, server.Client()).GetTorrent(context.Background(), hash)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "owned-secret") || calls.Load() != 1 {
				t.Fatalf("wrong failure: %v", err)
			}
		})
	}
	t.Run("backoff", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "37")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		err := rtorrent.NewClient(server.URL, server.Client()).Test(context.Background())
		limited, ok := ports.IsRateLimited(err)
		if !ok || limited.RetryAfter != 37*time.Second {
			t.Fatalf("lost backoff: %v", err)
		}
	})
}

func TestRTorrentRedirectAndLostReceiptNeverReplay(t *testing.T) {
	raw, hash := nativeMetainfo()
	var followed, calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	provided := redirect.Client()
	client := rtorrent.NewClient(redirect.URL, provided)
	p := ports.AddTorrentParam{InfoHash: hash, TorrentBytes: raw}
	if err := client.AddTorrent(context.Background(), p); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatalf("redirect: %v", err)
	}
	if calls.Load() != 1 || followed.Load() != 0 || provided.CheckRedirect != nil {
		t.Fatal("redirect followed or shared client mutated")
	}
	var attempts atomic.Int64
	lost := &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		attempts.Add(1)
		if r.GetBody != nil {
			t.Error("mutation body is replayable")
		}
		return nil, &url.Error{Op: "POST", URL: "https://owned-secret-password.invalid", Err: errors.New("owned-secret-password")}
	})}
	if err := rtorrent.NewClient("https://owned.invalid/RPC2", lost).AddTorrent(context.Background(), p); !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "owned-secret") {
		t.Fatalf("unsafe transport error: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatal("lost mutation receipt replayed")
	}
}

type nativeTransport func(*http.Request) (*http.Response, error)

func (f nativeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type nativeBlockingBody struct {
	ready, release chan struct{}
	once           sync.Once
	reader         io.Reader
	closed         atomic.Bool
}

func (b *nativeBlockingBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.ready); <-b.release })
	return b.reader.Read(p)
}
func (b *nativeBlockingBody) Close() error { b.closed.Store(true); return nil }

func TestRTorrentCancellationWaitsForPhysicalBody(t *testing.T) {
	raw, hash := nativeMetainfo()
	body := &nativeBlockingBody{ready: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(response(0))}
	client := rtorrent.NewClient("https://owned.invalid/RPC2", &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	done := make(chan struct{})
	var release sync.Once
	defer func() { cancel(); release.Do(func() { close(body.release) }); <-done }()
	go func() {
		defer close(done)
		result <- client.AddTorrent(ctx, ports.AddTorrentParam{InfoHash: hash, TorrentBytes: raw})
	}()
	select {
	case <-body.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("body read did not start")
	}
	cancel()
	select {
	case err := <-result:
		t.Fatalf("returned before physical read ended: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release.Do(func() { close(body.release) })
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("late success accepted: %v", err)
	}
	if !body.closed.Load() {
		t.Fatal("response body was not closed")
	}
}

func TestRTorrentConfigurationAndProxyAuthentication(t *testing.T) {
	for _, endpoint := range []string{"", "ftp://owned.invalid/RPC2", "https:///RPC2", "https://owned.invalid/RPC2?credential=secret", "https://owned.invalid/RPC2#fragment"} {
		if _, err := rtorrent.NewAuthenticatedClient(endpoint, "", "", nil); !errors.Is(err, ports.ErrBadResponse) {
			t.Fatal("invalid configuration accepted")
		}
	}
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		user, pass, ok := r.BasicAuth()
		if !ok || user != "proxy-user" || pass != "proxy-password" {
			t.Error("proxy authentication lost")
		}
		_, _ = io.WriteString(w, response("0.9.8"))
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.User = url.UserPassword("proxy-user", "proxy-password")
	if err := rtorrent.NewClient(endpoint.String(), server.Client()).Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := rtorrent.NewAuthenticatedClient(endpoint.String(), "different", "password", server.Client()); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal("ambiguous credentials accepted")
	}
	if requests.Load() != 1 {
		t.Fatal("unexpected configuration I/O")
	}
}
