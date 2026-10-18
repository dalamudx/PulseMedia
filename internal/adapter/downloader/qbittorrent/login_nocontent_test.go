package qbittorrent

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNoContentLoginRequiresProtectedVersionProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"trusted-subnet", 200, "2.15.1", nil},
		{"not-authenticated", 403, "denied", ports.ErrAuthRequired},
		{"empty-proxy-reply", 200, "", ports.ErrBadResponse},
		{"html-proxy", 200, "<html>ok</html>", ports.ErrBadResponse},
		{"unknown-version", 200, "3.0.0", ports.ErrBadResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logins, probes := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					logins++
					w.WriteHeader(http.StatusNoContent)
				case "/api/v2/app/webapiVersion":
					probes++
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				default:
					t.Error("unexpected endpoint")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, e := NewClient(server.URL, "", "", server.Client())
			if e != nil {
				t.Fatal(e)
			}
			e = client.Test(context.Background())
			if !errors.Is(e, tc.want) {
				t.Fatalf("login result %v want %v", e, tc.want)
			}
			if logins != 1 || probes != 1 {
				t.Fatalf("proof counts: login=%d probe=%d", logins, probes)
			}
			if client.loggedIn != (tc.want == nil) {
				t.Fatal("login state lacks expected version proof")
			}
		})
	}
}
