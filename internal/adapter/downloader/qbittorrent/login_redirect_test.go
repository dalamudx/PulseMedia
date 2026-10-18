package qbittorrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCredentialLoginRedirect(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); _, _ = w.Write([]byte("Ok.")) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/auth/login" {
			t.Error("inventory before login")
		}
		w.Header().Set("Location", target.URL)
		w.WriteHeader(307)
	}))
	defer source.Close()
	shared := &http.Client{}
	c, e := NewClient(source.URL, "fixture", "fixture-password", shared)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.GetTorrentFiles(context.Background(), strings.Repeat("a", 40)); e == nil {
		t.Fatal("redirect login succeeded")
	}
	if requests.Load() != 0 || shared.CheckRedirect != nil || shared.Jar != nil {
		t.Fatal("credentials forwarded or borrowed client mutated")
	}
}
