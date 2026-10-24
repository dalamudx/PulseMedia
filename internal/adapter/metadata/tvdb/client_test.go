package tvdb_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/tvdb"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const details = `{"status":"success","data":{"id":81189,"name":"Breaking Bad","year":"2008","originalLanguage":"eng","score":99999,"seasons":[{"id":301,"number":1,"name":"Season 1","type":{"id":1,"type":"official"}},{"id":302,"number":1,"type":{"id":2,"type":"dvd"}}],"remoteIds":[{"id":"1396","type":12,"sourceName":"TheMovieDB.com"}]}}`

func native(t *testing.T, pin string, handler http.HandlerFunc) (*tvdb.Client, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v4/login" {
			var body map[string]string
			if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || json.NewDecoder(r.Body).Decode(&body) != nil || body["apikey"] != "owned-api-key" || body["pin"] != pin || len(body) != (1+map[bool]int{true: 1}[pin != ""]) {
				t.Error("incorrect native login or credential isolation")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"status":"success","data":{"token":"owned-bearer"}}`)
			return
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer owned-bearer" || strings.Contains(r.URL.RawQuery, "key") {
			t.Error("native read did not use login token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	base := server.Client().Transport
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api4.thetvdb.com" || r.URL.User != nil {
			return nil, errors.New("unexpected native destination")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		clone.URL = &u
		return base.RoundTrip(clone)
	})}
	return tvdb.NewClientWithPIN("owned-api-key", pin, client), calls
}
func TestTheTVDBSeriesFetching(t *testing.T) {
	c, calls := native(t, "owned-pin", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v4/series/81189/extended" {
			t.Error("wrong series path")
		}
		_, _ = io.WriteString(w, details)
	})
	got, err := c.GetSeriesDetails(t.Context(), 81189)
	if err != nil || got == nil || got.TVDBID != 81189 || got.Title != "Breaking Bad" || got.Year != 2008 || got.OriginalLanguage != "eng" || len(got.Seasons) != 2 || got.Seasons[0].EpisodeCount != nil || got.Seasons[1].Numbering != "dvd" || got.RemoteIDs[0].ID != "1396" || calls.Load() != 2 {
		t.Fatalf("native facts or unknown count changed: %+v %v calls=%d", got, err, calls.Load())
	}
}
func TestTVDBStrictSeriesResponses(t *testing.T) {
	for name, body := range map[string]string{
		"wrong_identity":        strings.Replace(details, `"id":81189`, `"id":550`, 1),
		"duplicate_identity":    strings.Replace(details, `"id":81189`, `"id":81189,"ID":550`, 1),
		"wrong_business_status": strings.Replace(details, `"success"`, `"failure"`, 1),
		"trailing":              details + `{}`,
		"missing_data":          `{"status":"success"}`,
		"null_data":             `{"status":"success","data":null}`,
		"fractional_id":         strings.Replace(details, `81189`, `81189.5`, 1),
		"missing_season_number": strings.Replace(details, `"number":1,`, "", 1),
		"bad_year":              strings.Replace(details, `"2008"`, `"0000"`, 1),
		"oversized":             `{"status":"success","data":{"overview":"` + strings.Repeat("x", 5<<20) + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := native(t, "", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
			got, err := c.GetSeriesDetails(t.Context(), 81189)
			if got != nil || !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("malformed facts accepted: %v", err)
			}
		})
	}
	c, _ := native(t, "", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"success","data":{"id":81189,"name":"Unknown","year":null}}`)
	})
	got, err := c.GetSeriesDetails(t.Context(), 81189)
	if err != nil || got.Year != 0 || got.Seasons != nil {
		t.Fatalf("missing values fabricated: %+v %v", got, err)
	}
}
func TestTVDBNativeErrorsAndBackoff(t *testing.T) {
	cases := []struct {
		name   string
		status int
		retry  string
		want   error
		delay  time.Duration
	}{
		{"unauthorized", http.StatusUnauthorized, "", ports.ErrAuthRequired, 0},
		{"forbidden", http.StatusForbidden, "", ports.ErrAuthRequired, 0},
		{"not_found", http.StatusNotFound, "", ports.ErrNotFound, 0},
		{"unavailable", http.StatusServiceUnavailable, "", ports.ErrUnavailable, 0},
		{"redirect", http.StatusFound, "", ports.ErrBadResponse, 0},
		{"seconds", http.StatusTooManyRequests, "19", nil, 19 * time.Second},
		{"date", http.StatusTooManyRequests, "Wed, 21 Oct 2015 07:28:20 GMT", nil, 20 * time.Second},
		{"overflow", http.StatusTooManyRequests, "999999999999999999999", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, calls := native(t, "", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", tc.retry)
				w.Header().Set("Date", "Wed, 21 Oct 2015 07:28:00 GMT")
				w.Header().Set("Location", "https://untrusted.example/credential")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, "secret raw body")
			})
			_, err := c.GetSeriesDetails(t.Context(), 81189)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("wrong error: %v", err)
				}
			} else {
				var limited *ports.RateLimitError
				if !errors.As(err, &limited) || limited.RetryAfter != tc.delay {
					t.Fatalf("wrong backoff: %v", err)
				}
			}
			if calls.Load() != 2 || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "https") {
				t.Fatal("replayed request or leaked upstream detail")
			}
		})
	}
}
func TestTVDBLoginFailureAndInputBoundaries(t *testing.T) {
	for _, body := range []string{`{"status":"success","data":{"token":""}}`, `{"status":"failure","data":{"token":"x"}}`, `{"status":"success","data":{"token":"x","Token":"y"}}`, `{"status":"success","data":{"token":"bad\r\nheader"}}`} {
		var calls atomic.Int32
		c := tvdb.NewClient("owned-key", &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			if r.URL.Path != "/v4/login" {
				t.Error("sent read after failed login")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
		if _, err := c.GetSeriesDetails(t.Context(), 81189); !errors.Is(err, ports.ErrBadResponse) || calls.Load() != 1 {
			t.Fatalf("invalid login accepted: %v", err)
		}
	}
	c := tvdb.NewClient("bad\nkey", &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		t.Error("invalid input sent network request")
		return nil, errors.New("unexpected")
	})})
	if _, err := c.GetSeriesDetails(t.Context(), 81189); !errors.Is(err, ports.ErrAuthRequired) {
		t.Fatal(err)
	}
	if _, err := c.GetSeriesDetails(t.Context(), 0); !errors.Is(err, ports.ErrRejected) {
		t.Fatal(err)
	}
	if _, err := c.GetSeriesTranslation(t.Context(), 81189, "../"); !errors.Is(err, ports.ErrRejected) {
		t.Fatal(err)
	}
	if _, err := c.GetSeasonMetadata(t.Context(), 81189, -1); !errors.Is(err, ports.ErrRejected) {
		t.Fatal(err)
	}
}
func episode(id, number int, aired string) string {
	return fmt.Sprintf(`{"id":%d,"seriesId":81189,"seasonNumber":1,"number":%d,"aired":%q,"absoluteNumber":0}`, id, number, aired)
}
func pageBody(episodes, next string, total int) string {
	return fmt.Sprintf(`{"status":"success","data":{"series":{"id":81189},"episodes":[%s]},"links":{"next":%s,"total_items":%d}}`, episodes, next, total)
}
func TestTVDBSparseOfficialPagination(t *testing.T) {
	c, calls := native(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v4/series/81189/episodes/official" || r.URL.Query().Get("season") != "1" {
			t.Error("not explicit official numbering")
		}
		switch r.URL.Query().Get("page") {
		case "0":
			_, _ = io.WriteString(w, pageBody(episode(1001, 2, "2008-01-20"), `"https://api4.thetvdb.com/v4/series/81189/episodes/official?page=1&season=1"`, 2))
		case "1":
			_, _ = io.WriteString(w, pageBody(episode(1002, 7, ""), `null`, 2))
		default:
			t.Error("unexpected page")
			http.Error(w, "invalid page", http.StatusBadRequest)
		}
	})
	got, err := c.GetSeasonMetadata(t.Context(), 81189, 1)
	if err != nil || !got.Complete || got.Numbering != "official" || len(got.Episodes) != 2 || got.Episodes[0].EpisodeNumber != 2 || got.Episodes[1].EpisodeNumber != 7 || got.Episodes[0].AirDate == nil || got.Episodes[1].AirDate != nil || got.Episodes[0].AbsoluteNumber != nil || calls.Load() != 3 {
		t.Fatalf("invalid sparse snapshot: %+v %v", got, err)
	}
}
func TestTVDBPaginationRejectsIncompleteOrForeignFacts(t *testing.T) {
	good := pageBody(episode(1001, 2, ""), `null`, 1)
	for name, body := range map[string]string{
		"missing_links":      `{"status":"success","data":{"series":{"id":81189},"episodes":[]}}`,
		"missing_next":       strings.Replace(good, `"next":null,`, "", 1),
		"wrong_parent":       strings.Replace(good, `"seriesId":81189`, `"seriesId":550`, 1),
		"wrong_season":       strings.Replace(good, `"seasonNumber":1`, `"seasonNumber":2`, 1),
		"wrong_series":       strings.Replace(good, `"series":{"id":81189}`, `"series":{"id":550}`, 1),
		"foreign_next":       pageBody(episode(1001, 2, ""), `"https://foreign.example/v4/series/81189/episodes/official?page=1&season=1"`, 2),
		"skipped_page":       pageBody(episode(1001, 2, ""), `"/v4/series/81189/episodes/official?page=2&season=1"`, 2),
		"wrong_numbering":    pageBody(episode(1001, 2, ""), `"/v4/series/81189/episodes/dvd?page=1&season=1"`, 2),
		"duplicate":          pageBody(episode(1001, 2, "")+","+episode(1002, 2, ""), `null`, 2),
		"invalid_date":       pageBody(episode(1001, 2, "2026-02-30"), `null`, 1),
		"missing_count":      pageBody(episode(1001, 2, ""), `null`, 2),
		"empty_continuation": pageBody("", `"/v4/series/81189/episodes/official?page=1&season=1"`, 2),
	} {
		t.Run(name, func(t *testing.T) {
			c, calls := native(t, "", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
			got, err := c.GetSeasonMetadata(t.Context(), 81189, 1)
			if got != nil || !errors.Is(err, ports.ErrBadResponse) || calls.Load() != 2 {
				t.Fatalf("unsafe partial snapshot: %v calls=%d", err, calls.Load())
			}
		})
	}
	c, _ := native(t, "", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, pageBody("", `null`, 0)) })
	got, err := c.GetSeasonMetadata(t.Context(), 81189, 1)
	if err != nil || !got.Complete || len(got.Episodes) != 0 {
		t.Fatalf("explicit empty differs from failure: %v", err)
	}
}
func TestTVDBTranslations(t *testing.T) {
	for _, language := range []string{"zho", "eng"} {
		c, _ := native(t, "", func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/translations/zho") {
				t.Error("wrong requested language")
			}
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"language":%q,"name":"绝命毒师","overview":""}}`, language)
		})
		got, err := c.GetSeriesTranslation(t.Context(), 81189, "zho")
		if language == "zho" {
			if err != nil || got.TVDBID != 81189 || got.Title != "绝命毒师" {
				t.Fatalf("translation lost identity: %v", err)
			}
		} else if got != nil || !errors.Is(err, ports.ErrBadResponse) {
			t.Fatalf("foreign translation accepted: %v", err)
		}
	}
}
func TestTVDBCancellationAndSanitizedTransportErrors(t *testing.T) {
	c := tvdb.NewClient("owned-key", &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("credential=https://private.example/secret")
	})})
	if _, err := c.GetSeriesDetails(t.Context(), 81189); !errors.Is(err, ports.ErrUnavailable) || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe network error: %v", err)
	}
	started := make(chan struct{})
	c, _ = native(t, "", func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.GetSeriesDetails(ctx, 81189); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not finish physical read")
	}
}
