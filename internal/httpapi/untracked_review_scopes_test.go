package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type reviewScopeHTTPFake struct {
	*untrackedReviewHTTPFake
	page      ports.UntrackedReviewScopePage
	listCalls int
	cursor    string
}

func (f *reviewScopeHTTPFake) ScopeOptions(_ context.Context, a *ports.Actor, cursor string) (ports.UntrackedReviewScopePage, error) {
	f.actor = *a
	f.listCalls++
	f.cursor = cursor
	return f.page, f.err
}

func TestUntrackedReviewScopeListHTTPTransport(t *testing.T) {
	for _, tc := range []struct {
		name, method, suffix, body, key string
		valid                           bool
	}{
		{"valid", "GET", "", "", "", true},
		{"cursor", "GET", "?cursor=" + untrackedScopeID, "", "", true},
		{"head", "HEAD", "", "", "", false},
		{"default", "OPTIONS", "", "", "", false},
		{"post", "POST", "", "", "", false},
		{"query", "GET", "?limit=100", "", "", false},
		{"empty_cursor", "GET", "?cursor=", "", "", false},
		{"duplicate", "GET", "?cursor=" + untrackedScopeID + "&cursor=" + untrackedScopeID, "", "", false},
		{"body", "GET", "", "x", "", false},
		{"key", "GET", "", "", "unexpected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, untrackedScopePrefix+tc.suffix, strings.NewReader(tc.body))
			if tc.key != "" {
				r.Header.Set("Idempotency-Key", tc.key)
			}
			if got := validUntrackedReviewScopesTransport(r); got != tc.valid {
				t.Fatalf("got=%t want=%t", got, tc.valid)
			}
		})
	}
}

func TestUntrackedReviewScopeListHTTPRoute(t *testing.T) {
	s, old := untrackedReviewHTTPFixture()
	fake := &reviewScopeHTTPFake{untrackedReviewHTTPFake: old, page: ports.UntrackedReviewScopePage{Items: []ports.UntrackedReviewScopeOption{{ID: untrackedScopeID, Version: 1, Enabled: true, LocalRoot: "/owned/source", TargetRootPath: "/owned/target", TargetMediaType: "tv"}}}}
	s.SetUntrackedReviewService(fake)
	h := untrackedReviewHTTPRouter(s, untrackedScopeAdmin())
	w := untrackedScopeCall(h, http.MethodGet, untrackedScopePrefix, "")
	untrackedReviewHTTPStatus(t, w, 200)
	if fake.listCalls != 1 || !strings.Contains(w.Body.String(), "/owned/source") || strings.Contains(w.Body.String(), "owner_id") || strings.Contains(w.Body.String(), "client_id") {
		t.Fatal("safe owner summaries not wired", w.Body.String())
	}
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		w = untrackedScopeCall(h, method, untrackedScopePrefix, "")
		if w.Code < 400 || fake.listCalls != 1 {
			t.Fatal("list method bypass", method, w.Code)
		}
	}
	noActor := untrackedReviewHTTPRouter(s, nil)
	w = untrackedScopeCall(noActor, http.MethodGet, untrackedScopePrefix, "")
	untrackedReviewHTTPStatus(t, w, http.StatusUnauthorized)
	if fake.listCalls != 1 {
		t.Fatal("unauthenticated list reached usecase")
	}
}
