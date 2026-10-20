package douban

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWishPartialVisibleWindow(t *testing.T) {
	for _, c := range []struct {
		name                  string
		start, total, visible int
	}{
		{"first_one_missing", 0, 169, 14},
		{"first_all_unavailable", 0, 169, 0},
		{"last_one_missing", 165, 169, 3},
		{"last_all_unavailable", 165, 169, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := wishFixture("tv", c.start, c.total)
			for remaining := min(15, c.total-c.start); remaining > c.visible; remaining-- {
				begin := strings.Index(body, `<div class="item comment-item">`)
				const endTag = "</ul></div></div>"
				if begin < 0 {
					t.Fatal("missing fixture item")
				}
				end := strings.Index(body[begin:], endTag)
				if end < 0 {
					t.Fatal("missing fixture item end")
				}
				body = body[:begin] + body[begin+end+len(endTag):]
			}
			s := wishServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = io.WriteString(w, body)
			})
			p, err := NewWishClient(s.URL, nil).ReadWishPage(t.Context(), "exherb", "tv", c.start)
			if err != nil || p.WindowComplete || len(p.Items) != c.visible || p.WindowEnd != min(c.start+15, c.total) || p.Total != c.total {
				t.Fatalf("partial window: %+v error=%v", p, err)
			}
			if p.WindowEnd < c.total {
				if p.NextStart == nil || *p.NextStart != p.WindowEnd {
					t.Fatal("cursor used visible count")
				}
			} else if p.NextStart != nil {
				t.Fatal("last partial page has next")
			}
		})
	}
}
