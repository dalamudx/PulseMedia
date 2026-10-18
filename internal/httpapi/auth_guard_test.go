package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
)

func TestAuthAdmissionBudgetAndBodyLimit(t *testing.T) {
	now := clock.NewMock(time.Now())
	calls := 0
	handler := authAdmissionMiddleware(now)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("X-Forwarded-For", "198.51.100.1")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < 10; i++ {
		if w := request("/api/v1/auth/login", `{}`); w.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := request("/api/v1/auth/setup", `{}`); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "6" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("budget bypass: %d", w.Code)
	}
	if calls != 10 {
		t.Fatalf("rejected request reached handler: %d", calls)
	}
	now.Advance(6 * time.Second)
	if w := request("/api/v1/auth/login", `{}`); w.Code != http.StatusNoContent {
		t.Fatal("budget did not recover")
	}
	now.Advance(6 * time.Second)
	if w := request("/api/v1/auth/login", `{}`+strings.Repeat(" ", 16<<10)); w.Code != http.StatusBadRequest {
		t.Fatal("oversized suffix bypassed body limit")
	}
	if calls != 11 {
		t.Fatal("oversized request reached handler")
	}
	if w := request("/api/v1/auth/logout", ""); w.Code != http.StatusNoContent {
		t.Fatal("logout shared password budget")
	}
}

func TestAuthAdmissionLimitsConcurrentPasswordWork(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	h := authAdmissionMiddleware(clock.NewMock(time.Now()))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer func() { close(release); wg.Wait() }()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`)))
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("password slot did not start")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(`{}`)))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("parallel password work not bounded: %d", w.Code)
	}
}
