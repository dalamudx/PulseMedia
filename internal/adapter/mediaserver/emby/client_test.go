package emby_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/emby"
)

func TestEmbyClientIntegration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "valid_token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if r.URL.Path == "/System/Info" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"ServerName": "Emby Main", "Version": "4.8.0"}`)
			return
		}

		if r.URL.Path == "/Library/Refresh" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()

	// 1. Success with valid token
	client := emby.NewClient(server.URL, "valid_token", server.Client())
	if err := client.Test(ctx); err != nil {
		t.Fatalf("test emby failed: %v", err)
	}
	if err := client.RefreshPaths(ctx, []string{"/data/movies"}); err != nil {
		t.Fatalf("refresh emby failed: %v", err)
	}

	// 2. Auth failure
	badClient := emby.NewClient(server.URL, "bad_token", server.Client())
	if err := badClient.Test(ctx); err == nil {
		t.Fatal("expected auth error with bad token")
	}
}
