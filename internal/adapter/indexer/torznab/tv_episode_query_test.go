package torznab_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/indexer/torznab"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func nativeTVQuery() ports.TVEpisodeQuery {
	return ports.TVEpisodeQuery{SchemaVersion: "tv-episode-query-v1", Mode: "tvsearch", SeriesNamespace: "tmdb.tv", SeriesID: 42, Keyword: "Example 剧", Season: 2, Episode: 3, Categories: []int32{5040, 5000}, Limit: 50}
}

func TestTVEpisodeNativeProtocolAndCapabilities(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "configured-user" || password != "configured-secret" || r.URL.User != nil {
			t.Error("configured auth not isolated to header")
		}
		if r.URL.EscapedPath() != "/encoded%2Fsource/api" {
			t.Error("configured path lost")
		}
		if r.URL.Query().Get("t") == "caps" {
			_, _ = io.WriteString(w, `<caps><limits default="50" max="100"/><searching><tv-search available="yes" supportedParams="q,season,ep"/></searching></caps>`)
			return
		}
		offset := r.URL.Query().Get("offset")
		const prefix = "/encoded%2Fsource/api?apikey=" + "synthetic-tv-key" + "&cat=5000%2C5040&ep=3&keep=a&keep=b&limit=50&offset="
		const suffix = "&passkey=" + "kept%2Fvalue" + "&q=Example+%E5%89%A7&season=2&t=tvsearch"
		if r.RequestURI != prefix+offset+suffix {
			t.Errorf("native request differs: %s", r.RequestURI)
		}
		_, _ = fmt.Fprintf(w, `<rss><channel><response offset="%s" total="200"/><item><title> Example.S02E03 </title><guid> tv-guid </guid><link>ignored</link><enclosure url="https://example.invalid/download" length="100"/><pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate><attr name="size" value="200"/><attr name="infohash" value=" ABCD "/><attr name="seeders" value="7"/><attr name="leechers" value="2"/><attr name="category" value="5040"/></item></channel></rss>`, offset)
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL + "/encoded%2Fsource/api?keep=a&keep=b&passkey=kept%2Fvalue&t=movie&q=old&cat=2000&season=9&ep=9&offset=999&limit=1")
	if err != nil {
		t.Fatal(err)
	}
	endpoint.User = url.UserPassword("configured-user", "configured-secret")
	client := torznab.NewClient(endpoint.String(), "synthetic-tv-key", server.Client())
	caps, err := client.Capabilities(context.Background())
	if err != nil || !caps.TVSearch.Available || caps.MovieSearch.Available || !reflect.DeepEqual(caps.TVSearch.SupportedParams, []string{"q", "season", "ep"}) {
		t.Fatal("TV-only caps not decoded", caps, err)
	}
	for _, offset := range []int32{0, 50, 100, 150} {
		q := nativeTVQuery()
		q.Offset = offset
		page, err := client.SearchTVEpisodePage(context.Background(), q)
		if err != nil || page == nil {
			t.Fatal(err)
		}
		if page.Offset != offset || page.Limit != 50 || !page.OffsetVerified || page.Total == nil || *page.Total != 200 || len(page.Items) != 1 {
			t.Fatal("page proof changed", page)
		}
		r := page.Items[0]
		if r.Title != "Example.S02E03" || r.GUID != "tv-guid" || r.DownloadURL != "https://example.invalid/download" || r.SizeBytes != 200 || r.InfoHash != "abcd" || r.Seeders != 7 || r.Leechers != 2 || r.PublishDate == nil || !reflect.DeepEqual(r.Categories, []int32{5040}) {
			t.Fatal("shared release conversion changed", r)
		}
		if !reflect.DeepEqual(q.Categories, []int32{5040, 5000}) {
			t.Fatal("adapter mutated query")
		}
	}
	if calls.Load() != 5 {
		t.Fatal("unexpected automatic caps, retries or fallback", calls.Load())
	}
}

func TestTVEpisodeInvalidShapeSendsNoIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := torznab.NewClient(server.URL, "key", server.Client())
	cases := []func(*ports.TVEpisodeQuery){
		func(q *ports.TVEpisodeQuery) { q.SchemaVersion = "" }, func(q *ports.TVEpisodeQuery) { q.SchemaVersion = "v2" },
		func(q *ports.TVEpisodeQuery) { q.Mode = "" }, func(q *ports.TVEpisodeQuery) { q.Mode = "movie" },
		func(q *ports.TVEpisodeQuery) { q.SeriesNamespace = "" }, func(q *ports.TVEpisodeQuery) { q.SeriesNamespace = "tvdb.tv" },
		func(q *ports.TVEpisodeQuery) { q.SeriesID = 0 }, func(q *ports.TVEpisodeQuery) { q.Keyword = "" },
		func(q *ports.TVEpisodeQuery) { q.Keyword = " padded " }, func(q *ports.TVEpisodeQuery) { q.Keyword = "bad\x7f" },
		func(q *ports.TVEpisodeQuery) { q.Keyword = "bad\n" }, func(q *ports.TVEpisodeQuery) { q.Keyword = string([]byte{0xff}) },
		func(q *ports.TVEpisodeQuery) { q.Keyword = strings.Repeat("x", 513) },
		func(q *ports.TVEpisodeQuery) { q.Season = 0 }, func(q *ports.TVEpisodeQuery) { q.Season = 1000 },
		func(q *ports.TVEpisodeQuery) { q.Episode = 0 }, func(q *ports.TVEpisodeQuery) { q.Episode = 10000 },
		func(q *ports.TVEpisodeQuery) { q.Categories = nil }, func(q *ports.TVEpisodeQuery) { q.Categories = []int32{5000, 5000} },
		func(q *ports.TVEpisodeQuery) { q.Categories = []int32{0} }, func(q *ports.TVEpisodeQuery) { q.Categories = make([]int32, 101) },
		func(q *ports.TVEpisodeQuery) { q.Offset = -50 }, func(q *ports.TVEpisodeQuery) { q.Offset = 1 }, func(q *ports.TVEpisodeQuery) { q.Offset = 200 },
		func(q *ports.TVEpisodeQuery) { q.Limit = 0 }, func(q *ports.TVEpisodeQuery) { q.Limit = 100 },
	}
	for i, mutate := range cases {
		q := nativeTVQuery()
		mutate(&q)
		page, err := client.SearchTVEpisodePage(context.Background(), q)
		if !errors.Is(err, ports.ErrBadResponse) || page != nil {
			t.Fatalf("case %d accepted: %v", i, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid shape reached network")
	}
}

func TestTVEpisodeResponseProofAndSafeErrors(t *testing.T) {
	const item = `<item><title>Example.S02E03</title><guid>one</guid></item>`
	for _, tc := range []struct {
		name, body string
		status     int
		offset     int32
		want       error
	}{
		{"empty proof", `<rss><channel><response offset="0" total="0"/></channel></rss>`, 200, 0, nil},
		{"legacy empty", `<rss><channel/></rss>`, 200, 0, nil},
		{"past end", `<rss><channel><response offset="150" total="0"/></channel></rss>`, 200, 150, nil},
		{"wrong offset", `<rss><channel><response offset="0" total="51"/>` + item + `</channel></rss>`, 200, 50, ports.ErrBadResponse},
		{"duplicate offset", `<rss><channel><response offset="0"/><response offset="0"/></channel></rss>`, 200, 0, ports.ErrBadResponse},
		{"missing later proof", `<rss><channel/></rss>`, 200, 50, ports.ErrBadResponse},
		{"inconsistent total", `<rss><channel><response offset="0" total="1"/></channel></rss>`, 200, 0, ports.ErrBadResponse},
		{"invalid total", `<rss><channel><response offset="0" total="-1"/></channel></rss>`, 200, 0, ports.ErrBadResponse},
		{"over limit", `<rss><channel>` + strings.Repeat(item, 51) + `</channel></rss>`, 200, 0, ports.ErrBadResponse},
		{"malformed XML", `<rss><channel>synthetic-secret`, 200, 0, ports.ErrBadResponse},
		{"trailing root", `<rss><channel/></rss><error code="100"/>`, 200, 0, ports.ErrBadResponse},
		{"API auth", `<error code="100" description="synthetic-secret"/>`, 200, 0, ports.ErrAuthRequired},
		{"HTTP auth", `synthetic-secret`, 401, 0, ports.ErrAuthRequired},
		{"rate", `synthetic-secret`, 429, 0, nil},
		{"oversized", `<rss><channel><!--` + strings.Repeat("x", 5<<20) + `--></channel></rss>`, 200, 0, ports.ErrBadResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			q := nativeTVQuery()
			q.Offset = tc.offset
			page, err := torznab.NewClient(server.URL+"/api?passkey=synthetic-secret", "synthetic-secret", server.Client()).SearchTVEpisodePage(context.Background(), q)
			if tc.status == 429 {
				rate, ok := ports.IsRateLimited(err)
				if !ok || rate.RetryAfter != 17*time.Second {
					t.Fatal("rate advice lost", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatal("wrong response classification", err)
			}
			if err != nil {
				if page != nil || strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), q.Keyword) {
					t.Fatal("unsafe failure", err)
				}
			} else {
				if page == nil || page.Items == nil || len(page.Items) != 0 {
					t.Fatal("empty page lost", page)
				}
				if tc.name == "empty proof" && (!page.OffsetVerified || page.Total == nil || *page.Total != 0) {
					t.Fatal("empty proof lost")
				}
			}
			if calls.Load() != 1 {
				t.Fatal("fallback or retry occurred")
			}
		})
	}
}

func TestTVEpisodeRedirectAndSourceGuards(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	page, err := torznab.NewClient(origin.URL, "synthetic-secret", origin.Client()).SearchTVEpisodePage(context.Background(), nativeTVQuery())
	if !errors.Is(err, ports.ErrBadResponse) || page != nil || targetCalls.Load() != 0 {
		t.Fatal("redirect escaped configured source", err)
	}
	for _, endpoint := range []string{origin.URL + "/#fragment", origin.URL + "/%invalid", "file:///api"} {
		page, err := torznab.NewClient(endpoint, "synthetic-secret", origin.Client()).SearchTVEpisodePage(context.Background(), nativeTVQuery())
		if !errors.Is(err, ports.ErrBadResponse) || page != nil {
			t.Fatal("invalid source accepted", err)
		}
	}
}

func TestTVEpisodeCancellationAndTimeout(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if deadline {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			}
			done := make(chan error, 1)
			go func() {
				page, err := torznab.NewClient(server.URL, "key", server.Client()).SearchTVEpisodePage(ctx, nativeTVQuery())
				if page != nil {
					done <- errors.New("canceled request returned page")
					return
				}
				done <- err
			}()
			<-started
			if !deadline {
				cancel()
			}
			err := <-done
			want := error(context.Canceled)
			if deadline {
				want = ports.ErrTimeout
			}
			if !errors.Is(err, want) {
				t.Fatal("context classification lost", err)
			}
		})
	}
}

func TestTVEpisodeRejectsLateSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := false
	client := torznab.NewClient("http://synthetic.invalid", "synthetic-secret", &http.Client{Transport: torznabTransport(func(r *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &tvLateBody{Reader: strings.NewReader(`<rss><channel><response offset="0" total="0"/></channel></rss>`), closed: &closed}}, nil
	})})
	page, err := client.SearchTVEpisodePage(ctx, nativeTVQuery())
	if !errors.Is(err, context.Canceled) || page != nil || !closed {
		t.Fatal("late success accepted or body leaked", err)
	}
}

type tvLateBody struct {
	io.Reader
	closed *bool
}

func (b *tvLateBody) Close() error { *b.closed = true; return nil }
