package douban

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Minimal DOM shapes extracted from the captured public movie and TV pages.
func wishFixture(kind string, start, total int) string {
	label := "电影"
	if kind == "tv" {
		label = "电视"
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"></head><body><nav><a href="https://accounts.douban.com/passport/login">登录</a></nav>`)
	heading := "电影"
	if kind == "tv" {
		heading = "电视剧"
	}
	fmt.Fprintf(&b, `<div id="db-usr-profile"><div class="pic"><a href="/people/exherb/"><img src="https://img9.doubanio.com/icon/u3121296-54.jpg"></a></div><h1>exHerb想看的%s(%d)</h1></div>`, heading, total)
	fmt.Fprintf(&b, `<div class="tabs-more"><span class="gray">影片类型:</span><a class="lnk-tab-more"><span>%s</span></a><ul><li>全部</li></ul></div>`, label)
	first, last := start+1, min(start+15, total)
	if total == 0 {
		first = 0
		last = 0
	}
	fmt.Fprintf(&b, `<span class="subject-num">%d-%d&nbsp;/&nbsp;%d</span><div class="grid-view">`, first, last, total)
	for i := start; i < last; i++ {
		id := 26985973 + i
		fmt.Fprintf(&b, `<div class="item comment-item"><div class="pic"><a href="https://movie.douban.com/subject/%d/"><img></a></div><div class="info"><ul><li class="title"><a href="https://movie.douban.com/subject/%d/"><em>梅尔罗斯 / Patrick Melrose</em> / 浮生若梦</a><span>[可播放]</span></li><li class="intro">2018-05-12(美国) / 演员 / 60分钟</li></ul></div></div>`, id, id)
	}
	b.WriteString(`</div>`)
	if total > 15 {
		fmt.Fprintf(&b, `<div class="paginator"><span class="thispage" data-total-page="%d">%d</span>`, (total+14)/15, start/15+1)
		if last < total {
			fmt.Fprintf(&b, `<span class="next"><link rel="next" href="/people/exherb/wish?start=%d&amp;sort=time&amp;type=%s&amp;filter=all&amp;mode=grid"><a href="#">后页</a></span>`, last, kind)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</body></html>`)
	return b.String()
}

func wishServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

func TestWishPublicPages(t *testing.T) {
	cases := []struct {
		kind, uid    string
		start, total int
		double       bool
	}{
		{"movie", "exherb", 0, 909, false}, {"tv", "exherb", 0, 169, false},
		{"tv", "exherb", 15, 169, true}, {"tv", "exherb", 165, 169, false},
		{"tv", "3121296", 0, 169, false}, {"movie", "exherb", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s_%s_%d_%d", tc.kind, tc.uid, tc.start, tc.total), func(t *testing.T) {
			var calls atomic.Int32
			s := wishServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/people/"+tc.uid+"/wish" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Accept") != "text/html" {
					t.Errorf("unexpected request fields")
				}
				want := url.Values{"type": {tc.kind}, "start": {strconv.Itoa(tc.start)}, "sort": {"time"}, "filter": {"all"}, "mode": {"grid"}}
				if !reflect.DeepEqual(r.URL.Query(), want) {
					t.Errorf("unexpected query keys or values")
				}
				body := wishFixture(tc.kind, tc.start, tc.total)
				if tc.double {
					body = strings.ReplaceAll(body, "&amp;", "&amp;amp;")
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, body)
			})
			jar, _ := cookiejar.New(nil)
			origin, _ := url.Parse(s.URL)
			jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "private"}})
			original := &http.Client{Jar: jar}
			client := NewWishClient(s.URL, original)
			page, err := client.ReadWishPage(t.Context(), tc.uid, tc.kind, tc.start)
			if err != nil {
				t.Fatal(err)
			}
			if page.RequestedID != tc.uid || page.CanonicalID != "exherb" || page.ProfileNumber != "3121296" || page.MediaType != tc.kind || page.Start != tc.start || page.Total != tc.total || len(page.Items) != min(15, tc.total-tc.start) {
				t.Fatalf("wrong typed page: %+v", page)
			}
			if !page.WindowComplete || page.WindowEnd != min(tc.start+ports.DoubanWishPageSize, tc.total) {
				t.Fatalf("incorrect window completeness: %+v", page)
			}
			for _, item := range page.Items {
				if item.Year != 2018 || item.MediaType != tc.kind || !reflect.DeepEqual(item.Titles, []string{"梅尔罗斯", "Patrick Melrose", "浮生若梦"}) {
					t.Fatalf("wrong item: %+v", item)
				}
			}
			if tc.start+len(page.Items) < tc.total {
				if page.NextStart == nil || *page.NextStart != tc.start+len(page.Items) {
					t.Fatal("missing next offset")
				}
			} else if page.NextStart != nil {
				t.Fatal("unexpected next offset")
			}
			if calls.Load() != 1 || original.Jar != jar || original.Timeout != 0 || original.CheckRedirect != nil || client.httpClient.Timeout != 10*time.Second {
				t.Fatal("client policy mutated or extra I/O")
			}
			if len(page.Items) > 0 {
				page.Items[0].Titles[0] = "changed"
				again, e := client.ReadWishPage(t.Context(), tc.uid, tc.kind, tc.start)
				if e != nil || again.Items[0].Titles[0] != "梅尔罗斯" {
					t.Fatal("returned data retained mutable state")
				}
			}
		})
	}
}

func TestWishRejectsUnprovenPages(t *testing.T) {
	base := wishFixture("tv", 0, 169)
	cases := []struct{ name, old, new string }{
		{"wrong_type", "<span>电视</span>", "<span>电影</span>"},
		{"mixed_type", "<span>电视</span>", "<span>全部</span>"},
		{"wrong_owner", "/people/exherb/", "/people/other/"},
		{"owner_traversal", `href="/people/exherb/"`, `href="/people/exherb/following"`},
		{"owner_host", `href="/people/exherb/"`, `href="https://evil.example/people/exherb/"`},
		{"watched_list", "想看的电视剧(169)", "看过的电视剧(169)"},
		{"watching_list", "想看的电视剧(169)", "在看的电视剧(169)"},
		{"heading_count", "想看的电视剧(169)", "想看的电视剧(168)"},
		{"heading_scope", "想看的电视剧(169)", "想看的电影(169)"},
		{"missing_profile", `id="db-usr-profile"`, `id="other"`},
		{"duplicate_profile", `<nav>`, `<div id="db-usr-profile"></div><nav>`},
		{"missing_grid", `class="grid-view"`, `class="list-view"`},
		{"missing_range", `class="subject-num"`, `class="other"`},
		{"missing_scope", "影片类型:", "筛选影片:"},
		{"range_start", "1-15&nbsp;/&nbsp;169", "16-30 / 169"},
		{"range_count", "1-15&nbsp;/&nbsp;169", "1-14 / 169"},
		{"total_bound", "1-15&nbsp;/&nbsp;169", "1-15 / 1000001"},
		{"missing_item", `class="item comment-item"`, `class="other"`},
		{"nested_items", `<div class="grid-view">`, `<div class="grid-view"><section>`},
		{"duplicate_id", "26985974", "26985973"},
		{"subject_host", "https://movie.douban.com/subject/", "https://evil.example/subject/"},
		{"subject_userinfo", "https://movie.douban.com/subject/", "https://reader:" + "fake" + "@" + "movie.douban.com/subject/"},
		{"subject_query", "/subject/26985973/", "/subject/26985973/?token=secret"},
		{"subject_fragment", "/subject/26985973/", "/subject/26985973/#secret"},
		{"subject_empty_fragment", "/subject/26985973/", "/subject/26985973/#"},
		{"subject_encoded", "/subject/26985973/", "/subject/%32%36%39%38%35%39%37%33/"},
		{"subject_zero", "/subject/26985973/", "/subject/0/"},
		{"subject_leading_zero", "/subject/26985973/", "/subject/026985973/"},
		{"subject_text", "/subject/26985973/", "/subject/abc/"},
		{"picture_mismatch", `<a href="https://movie.douban.com/subject/26985973/"><img>`, `<a href="https://movie.douban.com/subject/99/"><img>`},
		{"empty_title", "梅尔罗斯 / Patrick Melrose</em> / 浮生若梦", "   </em>"},
		{"empty_first_alias", "梅尔罗斯 / Patrick Melrose</em> / 浮生若梦", " / Patrick Melrose</em>"},
		{"empty_last_alias", "梅尔罗斯 / Patrick Melrose</em> / 浮生若梦", "梅尔罗斯 / </em>"},
		{"empty_middle_alias", "梅尔罗斯 / Patrick Melrose</em> / 浮生若梦", "梅尔罗斯 / / Patrick Melrose</em>"},
		{"long_title", "梅尔罗斯", strings.Repeat("名", 172)},
		{"many_aliases", "梅尔罗斯", strings.Repeat("a / ", 9) + "b"},
		{"no_next", `rel="next"`, `rel="prev"`},
		{"next_offset", "start=15&amp;sort=", "start=30&amp;sort="},
		{"next_kind", "type=tv&amp;filter=", "type=movie&amp;filter="},
		{"next_owner", "/people/exherb/wish?", "/people/other/wish?"},
		{"next_collect", "/people/exherb/wish?", "/people/exherb/collect?"},
		{"next_host", "/people/exherb/wish?", "https://evil.example/people/exherb/wish?"},
		{"next_userinfo", "/people/exherb/wish?", "https://reader:" + "fake" + "@" + "movie.douban.com/people/exherb/wish?"},
		{"next_fragment", "mode=grid\"", "mode=grid#secret\""},
		{"next_empty_fragment", "mode=grid\"", "mode=grid#\""},
		{"next_filter", "filter=all", "filter=video"},
		{"next_mode", "mode=grid", "mode=list"},
		{"next_extra", "mode=grid", "mode=grid&amp;status=do"},
		{"next_duplicate", "type=tv", "type=tv&amp;type=tv"},
		{"next_encoded_path", "/people/exherb/wish?", "/people/%65xherb/wish?"},
		{"wrong_page", `data-total-page="12">1`, `data-total-page="12">2`},
		{"wrong_page_count", `data-total-page="12"`, `data-total-page="13"`},
		{"missing_paginator", `class="paginator"`, `class="other"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.ReplaceAll(base, tc.old, tc.new)
			if body == base {
				t.Fatal("test mutation did not apply")
			}
			s := wishServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, body)
			})
			page, err := NewWishClient(s.URL, nil).ReadWishPage(t.Context(), "exherb", "tv", 0)
			if !errors.Is(err, ports.ErrBadResponse) || !reflect.DeepEqual(page, ports.DoubanWishPage{}) {
				t.Fatalf("got page/error: %+v %v", page, err)
			}
		})
	}
}

func TestWishNumericOwnerBinding(t *testing.T) {
	for _, tc := range []struct {
		name, uid, old, new string
		valid               bool
	}{
		{"trusted_numeric", "3121296", "unused", "unused", true},
		{"numeric_canonical", "3121296", "exherb", "3121296", true},
		{"unrelated_numeric", "123", "unused", "unused", false},
		{"vanity_cannot_use_picture", "another", "unused", "unused", false},
		{"untrusted_image", "3121296", "img9.doubanio.com", "img9.doubanio.com.evil.example", false},
		{"http_image", "3121296", "https://img9", "http://img9", false},
		{"image_outside_profile", "3121296", `<img src="https://img9.doubanio.com/icon/u3121296-54.jpg">`, `</a></div></div><img src="https://img9.doubanio.com/icon/u3121296-54.jpg"><div><div><a>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.ReplaceAll(wishFixture("tv", 0, 1), tc.old, tc.new)
			s := wishServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, body)
			})
			_, err := NewWishClient(s.URL, nil).ReadWishPage(t.Context(), tc.uid, "tv", 0)
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ports.ErrBadResponse) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestWishYearEvidence(t *testing.T) {
	for _, tc := range []struct {
		intro string
		year  int
	}{
		{"2027(美国)", 2027}, {"0001 / 美国", 1}, {"9999", 9999}, {"2018-05-12", 2018}, {"2018 演员", 2018},
		{"演员 / 2018", 0}, {"未知", 0}, {"20180 / 演员", 0}, {"2018年", 0}, {"0000", 0}, {"", 0},
	} {
		t.Run(tc.intro, func(t *testing.T) {
			body := strings.ReplaceAll(wishFixture("tv", 0, 1), "2018-05-12(美国) / 演员 / 60分钟", tc.intro)
			s := wishServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, body)
			})
			page, err := NewWishClient(s.URL, nil).ReadWishPage(t.Context(), "exherb", "tv", 0)
			if err != nil || page.Items[0].Year != tc.year {
				t.Fatalf("unexpected year/error: %+v %v", page, err)
			}
		})
	}
}

func TestWishHTTPBoundaries(t *testing.T) {
	valid := wishFixture("tv", 0, 0)
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
		length                  string
		want                    error
	}{
		{"forbidden", "private", "text/html; charset=utf-8", 403, "", ports.ErrDoubanWishAccessDenied},
		{"server_error", "secret", "text/html; charset=utf-8", 500, "", ports.ErrBadResponse},
		{"not_found", "secret", "text/html; charset=utf-8", 404, "", ports.ErrBadResponse},
		{"json", valid, "application/json", 200, "", ports.ErrBadResponse},
		{"missing_charset", valid, "text/html", 200, "", ports.ErrBadResponse},
		{"wrong_charset", valid, "text/html; charset=gbk", 200, "", ports.ErrBadResponse},
		{"invalid_utf8", valid + "\xff", "text/html; charset=utf-8", 200, "", ports.ErrBadResponse},
		{"null", valid + "\x00", "text/html; charset=utf-8", 200, "", ports.ErrBadResponse},
		{"challenge", "<html><body>登录 / captcha</body></html>", "text/html; charset=utf-8", 200, "", ports.ErrBadResponse},
		{"malformed", "<div class=grid-view><div class=item>", "text/html; charset=utf-8", 200, "", ports.ErrBadResponse},
		{"declared_large", "", "text/html; charset=utf-8", 200, strconv.Itoa(maxBodyBytes + 1), ports.ErrBadResponse},
		{"stream_large", strings.Repeat(" ", maxBodyBytes+1), "text/html; charset=utf-8", 200, "", ports.ErrBadResponse},
		{"fake_empty", strings.ReplaceAll(valid, `class="grid-view"`, `class="missing"`), "text/html; charset=utf-8", 200, "", ports.ErrBadResponse},
		{"empty_next", strings.ReplaceAll(valid, "</body>", `<link rel="next" href="/people/exherb/wish?start=15&amp;type=tv"></body>`), "text/html; charset=utf-8", 200, "", ports.ErrBadResponse},
		{"last_next", strings.ReplaceAll(wishFixture("tv", 165, 169), "</body>", `<link rel="next" href="/people/exherb/wish?start=180&amp;type=tv"></body>`), "text/html; charset=utf-8", 200, "", ports.ErrBadResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := wishServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				if tc.length != "" {
					w.Header().Set("Content-Length", tc.length)
				}
				w.WriteHeader(tc.status)
				if tc.name == "stream_large" {
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, tc.body)
			})
			start := 0
			if tc.name == "last_next" {
				start = 165
			}
			_, err := NewWishClient(s.URL, nil).ReadWishPage(t.Context(), "exherb", "tv", start)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "exherb") || strings.Contains(err.Error(), s.URL) || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe error")
			}
		})
	}
	t.Run("rate_limit", func(t *testing.T) {
		s := wishServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) })
		_, err := NewWishClient(s.URL, nil).ReadWishPage(t.Context(), "exherb", "tv", 0)
		var rate *ports.RateLimitError
		if !errors.As(err, &rate) {
			t.Fatalf("missing rate limit type: %v", err)
		}
	})
}

func TestWishRedirectsNeverFollow(t *testing.T) {
	var second atomic.Int32
	target := wishServer(t, func(http.ResponseWriter, *http.Request) { second.Add(1) })
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var first atomic.Int32
			s := wishServer(t, func(w http.ResponseWriter, r *http.Request) {
				first.Add(1)
				http.Redirect(w, r, target.URL+"/secret", status)
			})
			_, err := NewWishClient(s.URL, &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { t.Error("caller redirect callback used"); return nil }}).ReadWishPage(t.Context(), "exherb", "tv", 0)
			if !errors.Is(err, ports.ErrBadResponse) || first.Load() != 1 || second.Load() != 0 {
				t.Fatalf("redirect boundary failed: %v", err)
			}
		})
	}
}

func TestWishInvalidInputNeverRequests(t *testing.T) {
	var calls atomic.Int32
	s := wishServer(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	for _, uid := range []string{"", "../x", "a/b", "a%2fb", "a?key=secret", "a#x", "a\n", "_a", "-a", "用户", strings.Repeat("a", 65)} {
		if _, err := NewWishClient(s.URL, nil).ReadWishPage(t.Context(), uid, "tv", 0); !errors.Is(err, ports.ErrRejected) {
			t.Fatalf("invalid UID accepted: %q", uid)
		}
	}
	for _, start := range []int{-15, 1, 15001, 15015} {
		if _, err := NewWishClient(s.URL, nil).ReadWishPage(t.Context(), "exherb", "tv", start); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("invalid offset accepted")
		}
	}
	for _, kind := range []string{"all", "do", "collect", ""} {
		if _, err := NewWishClient(s.URL, nil).ReadWishPage(t.Context(), "exherb", kind, 0); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("invalid kind accepted")
		}
	}
	for _, base := range []string{s.URL + "/people", s.URL + "?key=secret", s.URL + "#secret", "https://user:secret@example.com", "http://example.com", "file:///private", "https:///", "https://example.com?"} {
		if _, err := NewWishClient(base, nil).ReadWishPage(t.Context(), "exherb", "tv", 0); !errors.Is(err, ports.ErrRejected) {
			t.Fatal("invalid origin accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input caused I/O")
	}
	if c := NewWishClient("", nil); c.base.String() != wishPublicOrigin || c.httpClient.Jar != nil {
		t.Fatal("incorrect public default")
	}
}

func TestWishCancellationAndSafeNetworkErrors(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	s := wishServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewWishClient(s.URL, nil).ReadWishPage(ctx, "exherb", "tv", 0); !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatal("pre-cancellation ignored")
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := NewWishClient(s.URL, nil).ReadWishPage(ctx, "exherb", "tv", 0); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("body cancellation: %v", err)
	}
	t.Run("timeout", func(t *testing.T) {
		slow := wishServer(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		_, err := NewWishClient(slow.URL, &http.Client{Timeout: 30 * time.Millisecond}).ReadWishPage(t.Context(), "exherb", "tv", 0)
		if !errors.Is(err, ports.ErrTimeout) {
			t.Fatalf("timeout not redacted: %v", err)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		closed := httptest.NewServer(http.NotFoundHandler())
		address := closed.URL
		closed.Close()
		_, err := NewWishClient(address, nil).ReadWishPage(t.Context(), "exherb", "tv", 0)
		if err != ports.ErrUnavailable {
			t.Fatalf("network error not sentinel: %v", err)
		}
	})
}
