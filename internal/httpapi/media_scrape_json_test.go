package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMediaScrapeStrictJSON(t *testing.T) {
	for _, body := range []string{"", `null`, `[]`, `{}`, `{"all":false}`, `{"all":null}`, `{"all":"true"}`, `{"all":true,"media_id":"x"}`, `{"all":true,"all":true}`, `{"all":true} {}`, `{"all":true,"extra":0}`, strings.Repeat(" ", 1025) + `{"all":true}`} {
		t.Run(body[:min(len(body), 50)], func(t *testing.T) {
			called := false
			h := strictMediaScrapeJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) }))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/media/scrape", strings.NewReader(body)))
			if rr.Code != http.StatusBadRequest || called {
				t.Fatalf("code=%d called=%t", rr.Code, called)
			}
		})
	}
	called := 0
	h := strictMediaScrapeJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called++; w.WriteHeader(http.StatusNoContent) }))
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(method, "/api/v1/media/scrape", strings.NewReader(`{ "all" : true }`)))
		if r.Code != http.StatusNoContent || r.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(r.Code, r.Header())
		}
	}
	if called != 2 {
		t.Fatal(called)
	}
}
