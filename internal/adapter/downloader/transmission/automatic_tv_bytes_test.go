package transmission

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func automaticTVByteParam() ports.AddTorrentParam {
	info := []byte("d6:lengthi1e4:name7:episode12:piece lengthi1e6:pieces20:01234567890123456789e")
	hash := sha1.Sum(info)
	return ports.AddTorrentParam{
		TorrentBytes: append(append([]byte("d4:info"), info...), 'e'), InfoHash: hex.EncodeToString(hash[:]),
		SavePath: "/downloads/TV (2026)", Tags: "pulsemedia,pulsemedia:op:12345678-1234-4234-9234-123456789abc",
	}
}

func automaticTVAddedACK(hash string) string {
	return fmt.Sprintf(`{"result":"success","arguments":{"torrent-added":{"hashString":%q,"name":"episode","id":7}},"tag":1}`, hash)
}

func automaticTVByteHTTPClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := NewClient(s.URL, "native-user", "private-password", s.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}

func checkAutomaticTVAddRequest(t *testing.T, r *http.Request, p ports.AddTorrentParam) {
	t.Helper()
	user, password, ok := r.BasicAuth()
	if r.Method != http.MethodPost || r.URL.Path != "/transmission/rpc" || !ok || user != "native-user" || password != "private-password" ||
		r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" {
		t.Error("unexpected native Add method, headers, path or auth")
	}
	var request struct {
		Method    string                     `json:"method"`
		Tag       int                        `json:"tag"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Errorf("decode Add: %v", err)
		return
	}
	if request.Method != "torrent-add" || request.Tag != 1 || len(request.Arguments) != 4 {
		t.Error("must send only torrent-add and the four byte submission arguments")
	}
	var metainfo, directory string
	var labels []string
	var paused *bool
	if json.Unmarshal(request.Arguments["metainfo"], &metainfo) != nil || metainfo != base64.StdEncoding.EncodeToString(p.TorrentBytes) ||
		json.Unmarshal(request.Arguments["download-dir"], &directory) != nil || directory != p.SavePath ||
		json.Unmarshal(request.Arguments["labels"], &labels) != nil || !reflect.DeepEqual(labels, strings.Split(p.Tags, ",")) ||
		json.Unmarshal(request.Arguments["paused"], &paused) != nil || paused == nil || *paused {
		t.Error("Add must preserve original bytes, download directory and full labels, unpaused")
	}
}

func TestAutomaticTVByteSenderExactNativeRequest(t *testing.T) {
	p := automaticTVByteParam()
	var count atomic.Int32
	c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		checkAutomaticTVAddRequest(t, r, p)
		_, _ = io.WriteString(w, automaticTVAddedACK(p.InfoHash))
	})
	if err := c.AutomaticTVByteSender().AddTorrent(t.Context(), p); err != nil || count.Load() != 1 {
		t.Fatalf("Add requests=%d error=%v", count.Load(), err)
	}
}

func TestAutomaticTVByteSenderRejectsUncertainACK(t *testing.T) {
	p := automaticTVByteParam()
	good := automaticTVAddedACK(p.InfoHash)
	cases := map[string]string{
		"wrong hash":                 strings.Replace(good, p.InfoHash, strings.Repeat("a", 40), 1),
		"uppercase hash":             strings.Replace(good, p.InfoHash, strings.ToUpper(p.InfoHash), 1),
		"missing hash":               strings.Replace(good, `"hashString":"`+p.InfoHash+`",`, "", 1),
		"null hash":                  strings.Replace(good, `"`+p.InfoHash+`"`, "null", 1),
		"numeric hash":               strings.Replace(good, `"`+p.InfoHash+`"`, "7", 1),
		"missing name":               strings.Replace(good, `,"name":"episode"`, "", 1),
		"null name":                  strings.Replace(good, `"name":"episode"`, `"name":null`, 1),
		"empty name":                 strings.Replace(good, `"name":"episode"`, `"name":""`, 1),
		"control name":               strings.Replace(good, `"name":"episode"`, `"name":"ep\n"`, 1),
		"null id":                    strings.Replace(good, `"id":7`, `"id":null`, 1),
		"bad id":                     strings.Replace(good, `"id":7`, `"id":-1`, 1),
		"string id":                  strings.Replace(good, `"id":7`, `"id":"7"`, 1),
		"wrong tag":                  strings.Replace(good, `"tag":1`, `"tag":2`, 1),
		"null tag":                   strings.Replace(good, `"tag":1`, `"tag":null`, 1),
		"missing result":             strings.Replace(good, `"result":"success",`, "", 1),
		"null result":                strings.Replace(good, `"result":"success"`, `"result":null`, 1),
		"failed result":              strings.Replace(good, `"success"`, `"private server failure"`, 1),
		"empty arguments":            `{"result":"success","arguments":{}}`,
		"missing arguments":          `{"result":"success"}`,
		"null arguments":             `{"result":"success","arguments":null}`,
		"array arguments":            `{"result":"success","arguments":[]}`,
		"string arguments":           `{"result":"success","arguments":"added"}`,
		"null added":                 `{"result":"success","arguments":{"torrent-added":null}}`,
		"array added":                `{"result":"success","arguments":{"torrent-added":[]}}`,
		"string added":               `{"result":"success","arguments":{"torrent-added":"added"}}`,
		"duplicate torrent":          strings.Replace(good, "torrent-added", "torrent-duplicate", 1),
		"both outcomes":              strings.Replace(good, `"torrent-added":`, `"torrent-duplicate":null,"torrent-added":`, 1),
		"extra argument":             strings.Replace(good, `"torrent-added":`, `"other-task":{},"torrent-added":`, 1),
		"extra added field":          strings.Replace(good, `"id":7`, `"id":7,"other":1`, 1),
		"extra outer field":          strings.Replace(good, `"tag":1`, `"tag":1,"other":1`, 1),
		"duplicate outer":            strings.Replace(good, `"result":`, `"result":"success","result":`, 1),
		"duplicate arguments":        strings.Replace(good, `"arguments":`, `"arguments":{},"arguments":`, 1),
		"duplicate outcome":          strings.Replace(good, `"torrent-added":`, `"torrent-added":{},"torrent-added":`, 1),
		"duplicate hash":             strings.Replace(good, `"hashString":`, `"hashString":"bad","hashString":`, 1),
		"escaped duplicate hash":     strings.Replace(good, `"hashString":`, `"hash\u0053tring":"bad","hashString":`, 1),
		"nested duplicate extension": strings.Replace(good, `"id":7`, `"id":7,"extension":[{"x":1,"x":2}]`, 1),
		"case alias":                 strings.Replace(good, "hashString", "HashString", 1),
		"case outcome alias":         strings.Replace(good, "torrent-added", "Torrent-Added", 1),
		"trailing JSON":              good + `{}`,
		"truncated JSON":             good[:len(good)-1],
		"invalid utf8":               strings.Replace(good, "episode", "\xff", 1),
		"oversized":                  good + strings.Repeat(" ", 1<<20),
		"outer array":                `[]`,
		"outer null":                 `null`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var count atomic.Int32
			c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				checkAutomaticTVAddRequest(t, r, p)
				_, _ = io.WriteString(w, body)
			})
			err := c.AutomaticTVByteSender().AddTorrent(t.Context(), p)
			if !errors.Is(err, ports.ErrUnavailable) || count.Load() != 1 {
				t.Fatalf("uncertain ACK must hold without replay: requests=%d err=%v", count.Load(), err)
			}
		})
	}
}

func TestAutomaticTVByteSenderResponseBounds(t *testing.T) {
	p := automaticTVByteParam()
	for _, size := range []int{1 << 20, (1 << 20) + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ack := automaticTVAddedACK(p.InfoHash)
			c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, ack+strings.Repeat(" ", size-len(ack)))
			})
			err := c.AutomaticTVByteSender().AddTorrent(t.Context(), p)
			if (err == nil) != (size == 1<<20) {
				t.Fatalf("size=%d err=%v", size, err)
			}
		})
	}
}

func TestAutomaticTVByteSenderRejectsInputWithoutHTTP(t *testing.T) {
	changes := map[string]func(*ports.AddTorrentParam){
		"nil bytes":           func(p *ports.AddTorrentParam) { p.TorrentBytes = nil },
		"empty bytes":         func(p *ports.AddTorrentParam) { p.TorrentBytes = []byte{} },
		"oversized":           func(p *ports.AddTorrentParam) { p.TorrentBytes = make([]byte, ports.MaxTorrentMetainfoBytes+1) },
		"URL":                 func(p *ports.AddTorrentParam) { p.DownloadURL = "https://private.example/passkey" },
		"paused":              func(p *ports.AddTorrentParam) { p.Paused = true },
		"no hash":             func(p *ports.AddTorrentParam) { p.InfoHash = "" },
		"upper hash":          func(p *ports.AddTorrentParam) { p.InfoHash = strings.ToUpper(p.InfoHash) },
		"invalid hash":        func(p *ports.AddTorrentParam) { p.InfoHash = strings.Repeat("z", 40) },
		"v2 hash":             func(p *ports.AddTorrentParam) { p.InfoHash = strings.Repeat("a", 64) },
		"no path":             func(p *ports.AddTorrentParam) { p.SavePath = "" },
		"relative":            func(p *ports.AddTorrentParam) { p.SavePath = "downloads/TV" },
		"unclean":             func(p *ports.AddTorrentParam) { p.SavePath = "/downloads/../TV" },
		"control path":        func(p *ports.AddTorrentParam) { p.SavePath = "/downloads/TV\n" },
		"invalid utf8 path":   func(p *ports.AddTorrentParam) { p.SavePath = "/downloads/\xff" },
		"backslash path":      func(p *ports.AddTorrentParam) { p.SavePath = "/downloads\\TV" },
		"oversized path":      func(p *ports.AddTorrentParam) { p.SavePath = "/" + strings.Repeat("x", 4096) },
		"no tags":             func(p *ports.AddTorrentParam) { p.Tags = "" },
		"no op":               func(p *ports.AddTorrentParam) { p.Tags = "pulsemedia" },
		"extra tag":           func(p *ports.AddTorrentParam) { p.Tags += ",extra" },
		"duplicate tag":       func(p *ports.AddTorrentParam) { p.Tags += ",pulsemedia" },
		"duplicate operation": func(p *ports.AddTorrentParam) { p.Tags += "," + strings.Split(p.Tags, ",")[1] },
		"whitespace":          func(p *ports.AddTorrentParam) { p.Tags = " " + p.Tags },
		"control tag":         func(p *ports.AddTorrentParam) { p.Tags += "\n" },
		"zero op": func(p *ports.AddTorrentParam) {
			p.Tags = "pulsemedia,pulsemedia:op:00000000-0000-0000-0000-000000000000"
		},
		"uppercase op": func(p *ports.AddTorrentParam) { p.Tags = strings.Replace(p.Tags, "abc", "ABC", 1) },
		"bare op":      func(p *ports.AddTorrentParam) { p.Tags = "pulsemedia,12345678-1234-4234-9234-123456789abc" },
		"braced op": func(p *ports.AddTorrentParam) {
			p.Tags = "pulsemedia,pulsemedia:op:{12345678-1234-4234-9234-123456789abc}"
		},
	}
	var count atomic.Int32
	c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, _ *http.Request) { count.Add(1); w.WriteHeader(500) })
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p := automaticTVByteParam()
			change(&p)
			if err := c.AutomaticTVByteSender().AddTorrent(t.Context(), p); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("invalid input: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.AutomaticTVByteSender().AddTorrent(ctx, automaticTVByteParam()); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled: %v", err)
	}
	if count.Load() != 0 {
		t.Fatalf("invalid input sent %d requests", count.Load())
	}
}

func TestAutomaticTVByteSenderInclusivePayloadBounds(t *testing.T) {
	for _, size := range []int{1, ports.MaxTorrentMetainfoBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p := automaticTVByteParam()
			p.TorrentBytes = bytes.Repeat([]byte{0x80}, size)
			c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				checkAutomaticTVAddRequest(t, r, p)
				_, _ = io.WriteString(w, automaticTVAddedACK(p.InfoHash))
			})
			if err := c.AutomaticTVByteSender().AddTorrent(t.Context(), p); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAutomaticTVByteSenderSessionNegotiation(t *testing.T) {
	p := automaticTVByteParam()
	var requests, accepted atomic.Int32
	c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		checkAutomaticTVAddRequest(t, r, p)
		if r.Header.Get("X-Transmission-Session-Id") != "fixture-session" {
			w.Header().Set("X-Transmission-Session-Id", "fixture-session")
			w.WriteHeader(http.StatusConflict)
			return
		}
		accepted.Add(1)
		_, _ = io.WriteString(w, automaticTVAddedACK(p.InfoHash))
	})
	if err := c.AutomaticTVByteSender().AddTorrent(t.Context(), p); err != nil || requests.Load() != 2 || accepted.Load() != 1 {
		t.Fatalf("negotiation requests=%d accepted=%d err=%v", requests.Load(), accepted.Load(), err)
	}
}

func TestAutomaticTVByteSenderStatusFailuresDoNotRetry(t *testing.T) {
	for _, status := range []int{201, 204, 206, 301, 302, 303, 307, 308, 400, 401, 403, 404, 409, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var count atomic.Int32
			p := automaticTVByteParam()
			c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, _ *http.Request) {
				count.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, automaticTVAddedACK(p.InfoHash))
			})
			err := c.AutomaticTVByteSender().AddTorrent(t.Context(), p)
			if err == nil || count.Load() != 1 {
				t.Fatalf("status=%d requests=%d err=%v", status, count.Load(), err)
			}
			if (status == 401 || status == 403 || status == 409) && !errors.Is(err, ports.ErrAuthRequired) {
				t.Fatalf("auth classification: %v", err)
			}
		})
	}
	for _, session := range []string{"fixture-session", " "} {
		t.Run("repeated409-"+session, func(t *testing.T) {
			var count atomic.Int32
			c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, _ *http.Request) {
				count.Add(1)
				w.Header().Set("X-Transmission-Session-Id", session)
				w.WriteHeader(http.StatusConflict)
			})
			err := c.AutomaticTVByteSender().AddTorrent(t.Context(), automaticTVByteParam())
			limit := int32(2)
			if session == " " {
				limit = 1
			}
			if err == nil || count.Load() != limit {
				t.Fatalf("session requests=%d err=%v", count.Load(), err)
			}
		})
	}
}

func TestAutomaticTVByteSenderRedirectDoesNotForwardCredentials(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { forwarded.Add(1); w.WriteHeader(500) }))
	t.Cleanup(target.Close)
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, s := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", target.URL+"/private-password")
				w.WriteHeader(status)
			})
			var borrowedRedirects atomic.Int32
			s.Client().CheckRedirect = func(*http.Request, []*http.Request) error { borrowedRedirects.Add(1); return nil }
			if err := c.AutomaticTVByteSender().AddTorrent(t.Context(), automaticTVByteParam()); err == nil || strings.Contains(err.Error(), "private-password") {
				t.Fatalf("redirect: %v", err)
			}
			if s.Client().CheckRedirect == nil {
				t.Fatal("borrowed client changed")
			}
			_ = s.Client().CheckRedirect(nil, nil)
			if borrowedRedirects.Load() != 1 || forwarded.Load() != 0 {
				t.Fatal("redirect followed or borrowed client changed")
			}
		})
	}
}

func TestAutomaticTVByteSenderLostResponseDoesNotReplay(t *testing.T) {
	for _, mode := range []string{"EOF", "truncated-body", "content-range"} {
		t.Run(mode, func(t *testing.T) {
			p := automaticTVByteParam()
			var count atomic.Int32
			c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				checkAutomaticTVAddRequest(t, r, p)
				if mode == "EOF" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				ack := automaticTVAddedACK(p.InfoHash)
				if mode == "truncated-body" {
					w.Header().Set("Content-Length", fmt.Sprint(len(ack)+1))
				}
				if mode == "content-range" {
					w.Header().Set("Content-Range", "bytes 0-9/100")
				}
				_, _ = io.WriteString(w, ack)
			})
			err := c.AutomaticTVByteSender().AddTorrent(t.Context(), p)
			if !errors.Is(err, ports.ErrUnavailable) || count.Load() != 1 {
				t.Fatalf("requests=%d err=%v", count.Load(), err)
			}
		})
	}
}

func TestAutomaticTVByteSenderCancellationWaitsForCompleteResponse(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "client-timeout"} {
		t.Run(mode, func(t *testing.T) {
			p := automaticTVByteParam()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
				defer cancel()
			}
			var count atomic.Int32
			c, _ := automaticTVByteHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				checkAutomaticTVAddRequest(t, r, p)
				_, _ = io.WriteString(w, automaticTVAddedACK(p.InfoHash))
				w.(http.Flusher).Flush()
				if mode == "cancel" {
					cancel()
				}
				<-r.Context().Done()
			})
			if mode == "client-timeout" {
				c.httpClient.Timeout = 100 * time.Millisecond
			}
			err := c.AutomaticTVByteSender().AddTorrent(ctx, p)
			want := error(context.Canceled)
			if mode == "deadline" {
				want = context.DeadlineExceeded
			}
			if mode == "client-timeout" {
				want = ports.ErrTimeout
			}
			if !errors.Is(err, want) || count.Load() != 1 {
				t.Fatalf("requests=%d err=%v want=%v", count.Load(), err, want)
			}
		})
	}
}

func TestAutomaticTVByteSenderHTTP2DoesNotReplayReceivedAdd(t *testing.T) {
	var received atomic.Int32
	peerErrors := make(chan error, 1)
	s := httptest.NewUnstartedServer(nil)
	s.EnableHTTP2 = true
	s.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
		"h2": func(_ *http.Server, conn *tls.Conn, _ http.Handler) {
			if err := serveMutationResetPeer(conn, &received); err != nil {
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
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = c.AutomaticTVByteSender().AddTorrent(ctx, automaticTVByteParam())
	if !errors.Is(err, ports.ErrUnavailable) || received.Load() != 1 {
		t.Fatalf("received Add streams=%d err=%v", received.Load(), err)
	}
}
