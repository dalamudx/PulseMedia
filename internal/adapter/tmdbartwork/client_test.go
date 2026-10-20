package tmdbartwork

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type testBudget struct {
	slots    chan struct{}
	acquired atomic.Int32
	released atomic.Int32
	retry    atomic.Int64
}

func newBudget() *testBudget { return &testBudget{slots: make(chan struct{}, 1)} }

func (b *testBudget) Acquire(ctx context.Context) (func(), error) {
	select {
	case b.slots <- struct{}{}:
		b.acquired.Add(1)
		return func() { b.released.Add(1); <-b.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *testBudget) RecordRateLimit(d time.Duration) { b.retry.Store(int64(d)) }

func fixture(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 13, 19))
	for y := 0; y < 19; y++ {
		for x := 0; x < 13; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 15), G: uint8(y * 10), B: 75, A: 255})
		}
	}
	var out bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&out, img)
	} else {
		err = jpeg.Encode(&out, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// Only the test transport routes the fixed trusted URL to a real local TLS server.
func localClient(t *testing.T, handler http.HandlerFunc, budget ports.MetadataRequestBudget) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	wire := srv.Client()
	transport := wire.Transport
	wire.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "image.tmdb.org" || r.URL.Path != "/t/p/w342/asset.jpg" || r.URL.RawQuery != "" || r.URL.User != nil || r.Method != http.MethodGet {
			t.Error("request escaped trusted poster contract")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credential sent")
		}
		copyReq := r.Clone(r.Context())
		copyReq.URL.Scheme, copyReq.URL.Host = u.Scheme, u.Host
		return transport.RoundTrip(copyReq)
	})
	return New(wire, budget)
}

func unavailable(t *testing.T, data []byte, err error) {
	t.Helper()
	if data != nil || err != ports.ErrMetadataArtworkUnavailable || err.Error() != "metadata artwork unavailable" {
		t.Fatalf("expected nil bytes and safe sentinel, got bytes=%d err=%v", len(data), err)
	}
}

func TestFetchPosterJPEGAndPNG(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		t.Run(format, func(t *testing.T) {
			body := fixture(t, format)
			budget := newBudget()
			c := localClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain") // Validate actual bytes, not the claimed type.
				_, _ = w.Write(body)
			}, budget)
			data, err := c.FetchPoster(context.Background(), "/asset.jpg")
			if err != nil {
				t.Fatal(err)
			}
			img, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil || img.Bounds() != image.Rect(0, 0, 13, 19) || len(data) > maxBodyBytes {
				t.Fatal("invalid JPEG output", err)
			}
			if budget.acquired.Load() != 1 || budget.released.Load() != 1 {
				t.Fatal("unbalanced admission")
			}
		})
	}
}

func TestWrongOpaquePathsNeverAcquireOrRequest(t *testing.T) {
	budget := newBudget()
	var calls atomic.Int32
	c := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("private transport detail")
	})}, budget)
	for _, path := range []string{"", "https://image.tmdb.org/t/p/w342/asset.jpg", "https://evil.test/x.jpg", "//evil.test/x.jpg", "/../asset.jpg", "/a/b.jpg", "/asset.jpg?secret=x", "/asset.jpg#x", "/%61.jpg", "/asset.jpg\n", " /asset.jpg", "/asset.svg", "/a\\b.jpg"} {
		t.Run(path, func(t *testing.T) { data, err := c.FetchPoster(context.Background(), path); unavailable(t, data, err) })
	}
	if calls.Load() != 0 || budget.acquired.Load() != 0 {
		t.Fatal("invalid paths consumed admission or I/O")
	}
}

func TestRedirectMakesNoSecondRequest(t *testing.T) {
	var second atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { second.Add(1); w.WriteHeader(200) }))
	defer trap.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var first atomic.Int32
			budget := newBudget()
			c := localClient(t, func(w http.ResponseWriter, _ *http.Request) {
				first.Add(1)
				w.Header().Set("Location", strings.Replace(trap.URL, "://", "://user:private@", 1)+"/secret")
				w.WriteHeader(code)
			}, budget)
			data, err := c.FetchPoster(context.Background(), "/asset.jpg")
			unavailable(t, data, err)
			if first.Load() != 1 || second.Load() != 0 || budget.released.Load() != 1 {
				t.Fatal("redirect followed or permit leaked")
			}
		})
	}
}

func TestBadImages(t *testing.T) {
	pngBytes := fixture(t, "png")
	jpegBytes := fixture(t, "jpeg")
	bomb := func(width, height uint32) []byte {
		header := bytes.Clone(pngBytes[:33])
		binary.BigEndian.PutUint32(header[16:20], width)
		binary.BigEndian.PutUint32(header[20:24], height)
		binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))
		if _, err := png.DecodeConfig(bytes.NewReader(header)); err != nil {
			t.Fatal("bomb must have a valid config", err)
		}
		return header
	}
	cases := map[string][]byte{
		"empty": nil, "private-text": []byte("secret provider body"), "gif": []byte("GIF89a"),
		"webp": []byte("RIFFxxxxWEBPVP8 "), "truncated-png": pngBytes[:len(pngBytes)-12],
		"truncated-jpeg": jpegBytes[:len(jpegBytes)-15], "corrupt-jpeg": {0xff, 0xd8, 0, 1},
		"oversized":      bytes.Repeat([]byte{0}, maxBodyBytes+1),
		"dimension-bomb": bomb(maxDimension+1, 1), "pixel-bomb": bomb(3000, 3000),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			budget := newBudget()
			c := localClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/jpeg")
				w.WriteHeader(200)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				} // Unknown length exercises the streaming cap.
				_, _ = w.Write(body)
			}, budget)
			data, err := c.FetchPoster(context.Background(), "/asset.jpg")
			unavailable(t, data, err)
			if budget.released.Load() != 1 {
				t.Fatal("permit leaked")
			}
		})
	}
}

func TestNilAndZeroBudgetAndCanceledContext(t *testing.T) {
	var calls atomic.Int32
	wire := &http.Client{Timeout: 20 * time.Millisecond, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not run")
	})}
	for name, budget := range map[string]ports.MetadataRequestBudget{"nil": nil, "zero": &testBudget{}} {
		t.Run(name, func(t *testing.T) {
			data, err := New(wire, budget).FetchPoster(context.Background(), "/asset.jpg")
			unavailable(t, data, err)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	budget := newBudget()
	data, err := New(wire, budget).FetchPoster(ctx, "/asset.jpg")
	unavailable(t, data, err)
	if calls.Load() != 0 || budget.acquired.Load() != 0 {
		t.Fatal("closed admission made I/O")
	}
}

func TestConstructorClonesClient(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse("https://image.tmdb.org")
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: "credential", Value: "private"}})
	var redirects atomic.Int32
	original := &http.Client{Timeout: time.Hour, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { redirects.Add(1); return nil }}
	c := New(original, newBudget())
	if c.httpClient == original || original.Timeout != time.Hour || original.Jar != jar || c.httpClient.Jar != nil || c.httpClient.Timeout != requestTimeout {
		t.Fatal("client not safely cloned")
	}
	if err := original.CheckRedirect(nil, nil); err != nil || redirects.Load() != 1 {
		t.Fatal("original redirect callback changed")
	}
	if err := c.httpClient.CheckRedirect(nil, nil); err != http.ErrUseLastResponse || redirects.Load() != 1 {
		t.Fatal("clone redirects enabled")
	}
	for _, duration := range []time.Duration{0, -1, time.Hour, time.Second} {
		cloned := New(&http.Client{Timeout: duration}, newBudget())
		if cloned.httpClient.Timeout <= 0 || cloned.httpClient.Timeout > requestTimeout || (duration == time.Second && cloned.httpClient.Timeout != duration) {
			t.Fatal("unbounded timeout")
		}
	}
	if New(nil, nil).httpClient.Timeout != requestTimeout {
		t.Fatal("nil client has no finite timeout")
	}
}

type testBody struct {
	read  func([]byte) (int, error)
	close func() error
}

func (b *testBody) Read(p []byte) (int, error) { return b.read(p) }
func (b *testBody) Close() error               { return b.close() }

func TestCanceledUncooperativeWorkRetainsPhysicalPermit(t *testing.T) {
	for _, stage := range []string{"transport", "body", "close", "deadline"} {
		t.Run(stage, func(t *testing.T) {
			started, finish := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-finish:
				default:
					close(finish)
				}
			}()
			budget := newBudget()
			var closed atomic.Bool
			reader := bytes.NewReader(fixture(t, "png"))
			var blocked atomic.Bool
			block := func() {
				if blocked.CompareAndSwap(false, true) {
					close(started)
					<-finish
				}
			}
			body := &testBody{
				read: func(p []byte) (int, error) {
					if stage == "body" {
						block()
					}
					return reader.Read(p)
				},
				close: func() error {
					if stage == "close" {
						block()
					}
					closed.Store(true)
					return nil
				},
			}
			wire := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if stage == "transport" || stage == "deadline" {
					block()
				}
				return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
			})}
			c := New(wire, budget)
			ctx, cancel := context.WithCancel(context.Background())
			if stage == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				data, err := c.FetchPoster(ctx, "/asset.jpg")
				if len(data) != 0 {
					done <- errors.New("late output")
				} else {
					done <- err
				}
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("work did not start")
			}
			if stage == "deadline" {
				<-ctx.Done()
			} else {
				cancel()
			}
			waitCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer stop()
			if release, err := budget.Acquire(waitCtx); err == nil {
				release()
				t.Fatal("physical permit released early")
			}
			if budget.released.Load() != 0 || closed.Load() {
				t.Fatal("work was declared complete early")
			}
			select {
			case <-done:
				t.Fatal("uncooperative work returned early")
			default:
			}
			close(finish)
			select {
			case err := <-done:
				if err != ports.ErrMetadataArtworkUnavailable {
					t.Fatal("late success or unsafe error", err)
				}
			case <-time.After(time.Second):
				t.Fatal("work failed to join")
			}
			if !closed.Load() || budget.released.Load() != 1 {
				t.Fatal("body not closed before release")
			}
			release, err := budget.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}

func TestSafeFailuresCloseResponse(t *testing.T) {
	for _, name := range []string{"transport", "read", "close", "status", "content-length"} {
		t.Run(name, func(t *testing.T) {
			budget := newBudget()
			var closed atomic.Bool
			var reads atomic.Int32
			reader := bytes.NewReader(fixture(t, "jpeg"))
			body := &testBody{
				read: func(p []byte) (int, error) {
					reads.Add(1)
					if name == "read" {
						return 0, errors.New("private body error")
					}
					return reader.Read(p)
				},
				close: func() error {
					closed.Store(true)
					if name == "close" {
						return errors.New("private close error")
					}
					return nil
				},
			}
			c := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				if name == "transport" {
					return nil, errors.New("https://user:secret@private.test/?key=private")
				}
				resp := &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}
				if name == "status" {
					resp.StatusCode = 503
				}
				if name == "content-length" {
					resp.ContentLength = maxBodyBytes + 1
				}
				return resp, nil
			})}, budget)
			data, err := c.FetchPoster(context.Background(), "/asset.jpg")
			unavailable(t, data, err)
			if (name != "transport" && !closed.Load()) || budget.released.Load() != 1 {
				t.Fatal("physical cleanup missing")
			}
			if (name == "status" || name == "content-length") && reads.Load() != 0 {
				t.Fatal("read rejected body")
			}
		})
	}
}

func TestRateLimitSafeBoundedRetry(t *testing.T) {
	for raw, want := range map[string]time.Duration{"12": 12 * time.Second, "0": time.Second, "999999": 300 * time.Second, "18446744073709551615": 300 * time.Second, "overflow99999999999999999999": time.Second, "-1": time.Second, "private provider text": time.Second, "Wed, 21 Oct 2015 07:28:00 GMT": time.Second} {
		t.Run(raw, func(t *testing.T) {
			budget := newBudget()
			c := localClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", raw)
				w.WriteHeader(429)
				_, _ = io.WriteString(w, "private response")
			}, budget)
			data, err := c.FetchPoster(context.Background(), "/asset.jpg")
			unavailable(t, data, err)
			if time.Duration(budget.retry.Load()) != want || budget.released.Load() != 1 {
				t.Fatal("wrong rate budget or leaked permit")
			}
		})
	}
}

func TestJPEGWriterBoundAndCancellation(t *testing.T) {
	writer := posterWriter{ctx: context.Background(), data: make([]byte, maxBodyBytes-1, maxBodyBytes)}
	if n, err := writer.Write([]byte{1}); n != 1 || err != nil {
		t.Fatal("rejected boundary", err)
	}
	if n, err := writer.Write([]byte{2}); n != 0 || err != ports.ErrMetadataArtworkUnavailable || len(writer.data) != maxBodyBytes {
		t.Fatal("output cap failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writer = posterWriter{ctx: ctx}
	if n, err := writer.Write([]byte{1}); n != 0 || err != ports.ErrMetadataArtworkUnavailable || len(writer.data) != 0 {
		t.Fatal("canceled encoding wrote output")
	}
}
