package bangumi_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/metadata/bangumi"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func week(t *testing.T) string {
	t.Helper()
	days := []any{}
	for n := 1; n <= 7; n++ {
		items := []any{}
		if n == 1 {
			items = append(items, map[string]any{"id": 1001, "type": 2, "name": "葬送のフリーレン", "name_cn": "葬送的芙莉莲", "air_date": "2023-09-29", "images": map[string]any{"large": "http://lain.bgm.tv/pic/cover/l/frieren.jpg"}, "rating": map[string]any{"score": 8.9}})
		}
		days = append(days, map[string]any{"weekday": map[string]int{"id": n}, "items": items})
	}
	data, err := json.Marshal(days)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func native(t *testing.T, body string) (*http.Client, string) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/calendar" || r.Header.Get("Accept") != "application/json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "PulseMedia/") || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected native request")
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s.Client(), s.URL
}
func TestGetCalendar(t *testing.T) {
	c, u := native(t, week(t))
	days, err := bangumi.NewClient(u, c).GetCalendar(t.Context())
	if err != nil || len(days) != 7 {
		t.Fatal("week", err)
	}
	v := days[0].Items[0]
	if days[0].DayName != "星期一" || v.ID != 1001 || v.Title != "葬送的芙莉莲" || v.OrigTitle != "葬送のフリーレン" || v.Rating == nil || *v.Rating != 8.9 || v.AirDate != "2023-09-29" || v.PosterURL != "https://lain.bgm.tv/pic/cover/l/frieren.jpg" {
		t.Fatal("native facts lost")
	}
}
func TestBangumiPayloadBoundaries(t *testing.T) {
	base := week(t)
	bad := map[string]string{"null": "null", "object": "{}", "truncated": base[:len(base)-1], "trailing": base + "[]", "partial": `[{"weekday":{"id":1},"items":[]}]`, "invalid-utf8": string([]byte{255})}
	for _, x := range []struct{ key, from, to string }{{"weekday", `"id":1}`, `"id":0}`}, {"duplicate-day", `"id":2}`, `"id":1}`}, {"null-items", `"items":[]`, `"items":null`}, {"subject-id", `"id":1001`, `"id":0`}, {"kind", `"type":2`, `"type":6`}, {"missing-kind", `"type":2`, `"other":2`}, {"date", "2023-09-29", "2023-02-30"}, {"rating", `"score":8.9`, `"score":11`}, {"duplicate", `"id":1001`, `"id":1001,"id":1002`}, {"case-alias", `"id":1001`, `"id":1001,"ID":1002`}, {"title-type", `"name_cn":"葬送的芙莉莲"`, `"name_cn":123`}} {
		bad[x.key] = strings.Replace(base, x.from, x.to, 1)
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			c, u := native(t, body)
			if _, err := bangumi.NewClient(u, c).GetCalendar(t.Context()); !errors.Is(err, ports.ErrBadResponse) {
				t.Fatal("bad payload accepted", err)
			}
		})
	}
}
func TestBangumiUnknownsAndEmpty(t *testing.T) {
	for _, name := range []string{"empty", "date", "rating", "poster", "chinese"} {
		t.Run(name, func(t *testing.T) {
			body := week(t)
			switch name {
			case "empty":
				body = "[]"
			case "date":
				body = strings.Replace(body, "2023-09-29", "0000-00-00", 1)
			case "rating":
				body = strings.Replace(body, `"score":8.9`, `"score":0`, 1)
			case "poster":
				body = strings.Replace(body, "http://lain.bgm.tv/pic/cover/l/frieren.jpg", "https://private.example/private", 1)
			case "chinese":
				body = strings.Replace(body, `"name_cn":"葬送的芙莉莲"`, `"name_cn":""`, 1)
			}
			c, u := native(t, body)
			days, err := bangumi.NewClient(u, c).GetCalendar(t.Context())
			if err != nil || days == nil {
				t.Fatal(err)
			}
			if name == "empty" {
				if len(days) != 0 {
					t.Fatal("invented rows")
				}
				return
			}
			item := days[0].Items[0]
			if name == "date" && item.AirDate != "" || name == "rating" && item.Rating != nil || name == "poster" && item.PosterURL != "" || name == "chinese" && item.Title != item.OrigTitle {
				t.Fatal("unknown/fallback changed")
			}
		})
	}
}
func TestBangumiStatusRedaction(t *testing.T) {
	for _, code := range []int{401, 403, 429, 500, 503, 404, 204} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(code)
				_, _ = io.WriteString(w, "owned-private-upstream")
			}))
			defer s.Close()
			_, err := bangumi.NewClient(s.URL, s.Client()).GetCalendar(t.Context())
			if err == nil || strings.Contains(err.Error(), "owned-private-upstream") {
				t.Fatal("unsafe error")
			}
			want := ports.ErrBadResponse
			if code >= 500 {
				want = ports.ErrUnavailable
			}
			if code == 401 || code == 403 {
				want = ports.ErrAuthRequired
			}
			if code == 429 {
				var limited *ports.RateLimitError
				if !errors.As(err, &limited) || limited.RetryAfter != time.Minute {
					t.Fatal("retry advice lost", err)
				}
			} else if !errors.Is(err, want) {
				t.Fatal("wrong failure", err)
			}
		})
	}
}
func TestBangumiRedirectNoFollow(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer s.Close()
	_, err := bangumi.NewClient(s.URL, s.Client()).GetCalendar(t.Context())
	if !errors.Is(err, ports.ErrBadResponse) || calls.Load() != 0 {
		t.Fatal("followed redirect", err)
	}
}
func TestBangumiCancellationTimeoutInvalidConfig(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() }))
	defer s.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := bangumi.NewClient(s.URL, s.Client()).GetCalendar(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, u := range []string{"invalid", "https://user:secret@example.test", "https://example.test?q=private", "https://example.test/#private"} {
		if _, err := bangumi.NewClient(u, s.Client()).GetCalendar(t.Context()); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("invalid endpoint", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid inputs reached network")
	}
	c := s.Client()
	c.Timeout = 25 * time.Millisecond
	if _, err := bangumi.NewClient(s.URL, c).GetCalendar(t.Context()); !errors.Is(err, ports.ErrTimeout) {
		t.Fatal("timeout lost", err)
	}
	if calls.Load() != 1 {
		t.Fatal("request replayed")
	}
}
func TestBangumiResponseBound(t *testing.T) {
	c, u := native(t, strings.Repeat(" ", (2<<20)+1))
	if _, err := bangumi.NewClient(u, c).GetCalendar(t.Context()); !errors.Is(err, ports.ErrBadResponse) {
		t.Fatal("oversized body", err)
	}
}
func TestLiveBangumiCalendar(t *testing.T) {
	if os.Getenv("PULSE_BANGUMI_LIVE") != "1" {
		t.Skip("explicit public-network acceptance")
	}
	days, err := bangumi.NewClient("", nil).GetCalendar(t.Context())
	if err != nil || len(days) != 7 {
		t.Fatal("live calendar", err)
	}
	count := 0
	for _, day := range days {
		count += len(day.Items)
	}
	t.Logf("M10_BANGUMI_LIVE_NATIVE_PASS weekdays=%d subjects=%d TLS=VERIFIED redirects=DENIED retries=0", len(days), count)
}
