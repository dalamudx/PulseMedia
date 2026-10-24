package fanart

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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

const testKey = "fixture-fanart-secret"
const logoPath = "/fanart/movies/550/hdmovielogo/fixture.png"
const logoURL = imageOrigin + logoPath

var movieID = ports.ArtworkIdentity{MediaType: "movie", Provider: "tmdb", ID: 550}

func nativeFixture(t *testing.T, h http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	count := new(atomic.Int32)
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	hc := server.Client()
	base := hc.Transport
	hc.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		count.Add(1)
		if r.URL.Scheme != "https" || (r.URL.Host != "webservice.fanart.tv" && r.URL.Host != "assets.fanart.tv") {
			t.Errorf("unexpected native host")
		}
		clone := r.Clone(r.Context())
		clone.URL.Scheme = endpoint.Scheme
		clone.URL.Host = endpoint.Host
		return base.RoundTrip(clone)
	})
	return NewClient(testKey, hc), count
}
func movieJSON(imageURL string) string {
	return fmt.Sprintf(`{"tmdb_id":"550","hdmovielogo":[{"id":"1","url":%q,"lang":"zh"}]}`, imageURL)
}

func TestFanartNativeIdentityAndSelection(t *testing.T) {
	c, count := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("api_key") != testKey || r.Header.Get("Cookie") != "" {
			t.Error("wrong native request")
		}
		switch r.URL.Path {
		case "/v3/movies/550":
			_, _ = io.WriteString(w, `{"tmdb_id":"550","hdmovielogo":[{"id":"1","url":"https://assets.fanart.tv/fanart/movies/550/hdmovielogo/en.png","lang":"en"},{"id":"2","url":"https://assets.fanart.tv/fanart/movies/550/hdmovielogo/zh.png","lang":"zh"},{"id":"3","url":"https://assets.fanart.tv/fanart/movies/550/hdmovielogo/late.png","lang":"zh"}],"hdmovieclearart":[{"id":"4","url":"https://assets.fanart.tv/fanart/movies/550/hdmovieclearart/a.png"}],"moviebanner":[{"id":"5","url":"https://assets.fanart.tv/fanart/movies/550/moviebanner/a.jpg"}],"moviedisc":[{"id":"6","url":"https://assets.fanart.tv/fanart/movies/550/moviedisc/a.png"}]}`)
		case "/v3/tv/81189":
			_, _ = io.WriteString(w, `{"thetvdb_id":"81189","hdtvlogo":[{"id":"1","url":"https://assets.fanart.tv/fanart/tv/81189/hdtvlogo/a.png"}],"hdclearart":[{"id":"2","url":"https://assets.fanart.tv/fanart/tv/81189/hdclearart/a.png"}],"tvbanner":[]}`)
		default:
			t.Error("wrong identity endpoint")
			http.NotFound(w, r)
		}
	})
	movie, err := c.GetArtworks(t.Context(), movieID)
	if err != nil || movie.Identity != movieID || movie.ClearLogo != "/fanart/movies/550/hdmovielogo/zh.png" || movie.ClearArt == "" || movie.DiscArt == "" || movie.Banner == "" {
		t.Fatalf("movie selection: %+v %v", movie, err)
	}
	tvID := ports.ArtworkIdentity{MediaType: "tv", Provider: "tvdb", ID: 81189}
	tv, err := c.GetArtworks(t.Context(), tvID)
	if err != nil || tv.Identity != tvID || tv.ClearLogo == "" || tv.ClearArt == "" || tv.Banner != "" || tv.DiscArt != "" {
		t.Fatalf("TV identity: %+v %v", tv, err)
	}
	old, err := c.GetMovieArtworks(t.Context(), 550)
	if err != nil || old.ClearLogo != imageOrigin+movie.ClearLogo {
		t.Fatal("legacy projection", err)
	}
	if count.Load() != 3 {
		t.Fatal("unexpected retries", count.Load())
	}
}

func TestFanartInputAndHTTPClientIsolation(t *testing.T) {
	count := 0
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse(imageOrigin)
	jar.SetCookies(origin, []*http.Cookie{{Name: "private", Value: "never-send"}})
	supplied := &http.Client{Jar: jar, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { count++; return nil, errors.New("not called") })}
	c := NewClient(testKey, supplied)
	if supplied.Timeout != 0 || supplied.CheckRedirect != nil || supplied.Jar != jar || c.httpClient.Timeout != 10*time.Second || c.httpClient.Jar != nil {
		t.Fatal("mutated caller or missing bounds")
	}
	for _, id := range []ports.ArtworkIdentity{{}, {MediaType: "tv", Provider: "tmdb", ID: 550}, {MediaType: "movie", Provider: "tvdb", ID: 550}, {MediaType: "movie", Provider: "tmdb", ID: -1}} {
		if _, err := c.GetArtworks(t.Context(), id); !errors.Is(err, ports.ErrRejected) {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"", " ", "bad\r\nkey", strings.Repeat("x", 513)} {
		if _, err := NewClient(key, supplied).GetArtworks(t.Context(), movieID); !errors.Is(err, ports.ErrRejected) {
			t.Fatal(err)
		}
	}
	if count != 0 {
		t.Fatal("invalid input reached network")
	}
}

func TestFanartMalformedResponseRejected(t *testing.T) {
	good := movieJSON(logoURL)
	cases := map[string]string{
		"wrong-id": strings.Replace(good, `"550"`, `"551"`, 1), "wrong-namespace": strings.Replace(good, "tmdb_id", "thetvdb_id", 1),
		"absent-id": `{"hdmovielogo":[]}`, "numeric-id": `{"tmdb_id":550}`, "null": `null`, "null-array": `{"tmdb_id":"550","hdmovielogo":null}`,
		"duplicate-identity": `{"tmdb_id":"550","tmdb_id":"551"}`, "alias-identity": `{"tmdb_id":"550","TMDB_ID":"551"}`,
		"duplicate-row": strings.Replace(good, `"lang":"zh"`, `"lang":"zh","lang":"en"`, 1), "trailing": good + `{}`, "invalid-json": "{",
		"oversize": strings.Repeat(" ", maxJSONBytes+1), "bad-utf8": good + string([]byte{0xff}),
		"deep":         `{"tmdb_id":"550","extra":` + strings.Repeat("[", 34) + `0` + strings.Repeat("]", 34) + `}`,
		"bad-image-id": strings.Replace(good, `"id":"1"`, `"id":"0"`, 1), "duplicate-image": fmt.Sprintf(`{"tmdb_id":"550","hdmovielogo":[{"id":"1","url":%q},{"id":"1","url":%q}]}`, logoURL, logoURL),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c, n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
			if _, err := c.GetArtworks(t.Context(), movieID); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("wanted bad response: %v", err)
			}
			if n.Load() != 1 {
				t.Fatal("retried")
			}
		})
	}
}

func TestFanartAssetTrustAndWholeCandidateValidation(t *testing.T) {
	urls := []string{"http://assets.fanart.tv" + logoPath, "https://evil.invalid" + logoPath, "https://private" + "@" + "assets.fanart.tv" + logoPath, logoURL + "?key=secret", logoURL + "#x", imageOrigin + "/fanart/movies/551/hdmovielogo/a.png", imageOrigin + "/fanart/tv/550/hdmovielogo/a.png", imageOrigin + "/fanart/movies/550/hdmovielogo/../a.png", strings.Replace(logoURL, "fixture", "%66ixture", 1), strings.Replace(logoURL, ".png", ".jpg", 1)}
	for i, bad := range urls {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			body := fmt.Sprintf(`{"tmdb_id":"550","hdmovielogo":[{"id":"1","url":%q,"lang":"zh"},{"id":"2","url":%q,"lang":"en"}]}`, logoURL, bad)
			c, n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
			if _, err := c.GetArtworks(t.Context(), movieID); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatal("unsafe unselected candidate accepted", err)
			}
			if n.Load() != 1 {
				t.Fatal("unexpected IO")
			}
		})
	}
}

func TestFanartHTTPClassificationAndNoRedirect(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{{http.StatusNotFound, ports.ErrNotFound}, {http.StatusUnauthorized, ports.ErrAuthRequired}, {http.StatusForbidden, ports.ErrAuthRequired}, {http.StatusInternalServerError, ports.ErrUnavailable}, {http.StatusBadRequest, ports.ErrBadResponse}, {http.StatusFound, ports.ErrBadResponse}}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			c, n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://other.invalid/private")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, testKey)
			})
			_, err := c.GetArtworks(t.Context(), movieID)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), testKey) {
				t.Fatal("wrong safe classification", err)
			}
			if n.Load() != 1 {
				t.Fatal("redirect/retry")
			}
		})
	}
	c, _ := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"tmdb_id":"550"}`) })
	art, err := c.GetArtworks(t.Context(), movieID)
	if err != nil || art.Identity != movieID || art.ClearLogo != "" {
		t.Fatal("empty not distinguished", err)
	}
	for _, raw := range []string{"60", "-1", "86401", "Thu, 01 Jan 2026 00:01:00 GMT"} {
		t.Run(raw, func(t *testing.T) {
			c, _ := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Date", "Thu, 01 Jan 2026 00:00:00 GMT")
				w.Header().Set("Retry-After", raw)
				w.WriteHeader(http.StatusTooManyRequests)
			})
			_, err := c.GetArtworks(t.Context(), movieID)
			limited, ok := ports.IsRateLimited(err)
			want := time.Duration(0)
			if raw == "60" || strings.HasPrefix(raw, "Thu") {
				want = time.Minute
			}
			if !ok || limited.RetryAfter != want {
				t.Fatal("backoff lost", err)
			}
		})
	}
}

func TestFanartCancellationTimeoutAndSafeError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c, n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	if _, err := c.GetArtworks(ctx, movieID); !errors.Is(err, context.Canceled) || n.Load() != 0 {
		t.Fatal("cancelled request dispatched", err)
	}
	c.httpClient.Timeout = 35 * time.Millisecond
	if _, err := c.GetArtworks(t.Context(), movieID); !errors.Is(err, ports.ErrTimeout) {
		t.Fatal("timeout", err)
	}
	unsafe := NewClient(testKey, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("credential %s at private URL", testKey)
	})})
	if _, err := unsafe.GetArtworks(t.Context(), movieID); !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), testKey) {
		t.Fatal("leaked transport error", err)
	}
}

func pngFixture(t *testing.T) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	im.SetNRGBA(0, 0, color.NRGBA{R: 100, G: 60, B: 20, A: 70})
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestFanartArtworkBytesAndCredentialIsolation(t *testing.T) {
	pngData := pngFixture(t)
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	c, n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Header.Get("api-key") != "" || r.Header.Get("Authorization") != "" {
			t.Error("asset received credentials")
		}
		if strings.HasSuffix(r.URL.Path, ".png") {
			_, _ = w.Write(pngData)
		} else {
			_, _ = w.Write(jpg.Bytes())
		}
	})
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse(imageOrigin)
	jar.SetCookies(origin, []*http.Cookie{{Name: "private", Value: "sensitive"}})
	supplied := *c.httpClient
	supplied.Jar = jar
	c = NewClient(testKey, &supplied)
	out, err := c.FetchArtwork(t.Context(), logoPath)
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, alpha := im.At(0, 0).RGBA()
	if alpha != 70*257 {
		t.Fatal("lost alpha", alpha)
	}
	out, err = c.FetchArtwork(t.Context(), "/fanart/movies/550/moviebanner/a.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 {
		t.Fatal("extra requests")
	}
}

func TestFanartUnsafeImageRejected(t *testing.T) {
	data := pngFixture(t)
	huge := append([]byte(nil), data...)
	binary.BigEndian.PutUint32(huge[16:20], 100000)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	for name, body := range map[string][]byte{"invalid": []byte("not image"), "truncated": data[:len(data)/2], "oversize": bytes.Repeat([]byte{'x'}, maxImageBytes+1), "dimensions": huge} {
		t.Run(name, func(t *testing.T) {
			c, _ := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
			if _, err := c.FetchArtwork(t.Context(), logoPath); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatal(err)
			}
		})
	}
	c, n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) })
	if _, err := c.FetchArtwork(t.Context(), "/fanart/movies/550/moviebanner/a.jpg"); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal("format spoof", err)
	}
	for _, bad := range []string{logoURL, "/fanart/movies/550/hdmovielogo/../a.png", logoPath + "?x", logoPath + "#x", "/fanart/movies/0550/hdmovielogo/a.png", "/fanart/movies/550/hdmovielogo/%61.png"} {
		if _, err := c.FetchArtwork(t.Context(), bad); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("unsafe path", err)
		}
	}
	if n.Load() != 1 {
		t.Fatal("unsafe path reached network")
	}
}
