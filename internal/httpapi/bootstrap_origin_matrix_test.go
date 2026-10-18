package httpapi_test

import (
	"crypto/tls"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrapOriginSerializationAndCLICompatibility(t *testing.T) {
	cases := []struct {
		name, path, host    string
		origins             []string
		bearer, cookie, tls bool
		want                int
	}{
		{name: "originless local CLI", path: "setup", host: "localhost", want: 204},
		{name: "same origin", path: "setup", host: "localhost", origins: []string{"http://localhost"}, want: 204},
		{name: "same default port", path: "login", host: "localhost:80", origins: []string{"http://localhost"}, want: 204},
		{name: "same host case", path: "login", host: "LOCALHOST", origins: []string{"http://localhost"}, want: 204},
		{name: "foreign origin before cookie", path: "setup", host: "localhost", origins: []string{"https://foreign.invalid"}, want: 403},
		{name: "foreign origin bearer cannot bootstrap", path: "login", host: "localhost", origins: []string{"https://foreign.invalid"}, bearer: true, want: 403},
		{name: "opaque origin", path: "setup", host: "localhost", origins: []string{"null"}, want: 403},
		{name: "empty supplied origin", path: "login", host: "localhost", origins: []string{""}, want: 403},
		{name: "userinfo", path: "setup", host: "localhost", origins: []string{"http://user@localhost"}, want: 403},
		{name: "path", path: "login", host: "localhost", origins: []string{"http://localhost/"}, want: 403},
		{name: "query", path: "setup", host: "localhost", origins: []string{"http://localhost?hidden=x"}, want: 403},
		{name: "empty query", path: "setup", host: "localhost", origins: []string{"http://localhost?"}, want: 403},
		{name: "fragment", path: "login", host: "localhost", origins: []string{"http://localhost#x"}, want: 403},
		{name: "empty fragment", path: "setup", host: "localhost", origins: []string{"http://localhost#"}, want: 403},
		{name: "ambiguous origin", path: "setup", host: "localhost", origins: []string{"http://localhost", "https://foreign.invalid"}, want: 403},
		{name: "wrong port", path: "login", host: "localhost:8080", origins: []string{"http://localhost:8081"}, want: 403},
		{name: "TLS downgrade", path: "setup", host: "localhost", origins: []string{"http://localhost"}, tls: true, want: 403},
		{name: "TLS same origin", path: "login", host: "localhost:443", origins: []string{"https://localhost"}, tls: true, want: 204},
		{name: "cookie write missing origin", path: "logout", host: "localhost", cookie: true, want: 403},
		{name: "cookie write same origin", path: "logout", host: "localhost", cookie: true, origins: []string{"http://localhost"}, want: 204},
		{name: "ordinary bearer CLI", path: "logout", host: "localhost", cookie: true, bearer: true, want: 204},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/auth/"+tc.path, nil)
			req.Host = tc.host
			for _, origin := range tc.origins {
				req.Header.Add("Origin", origin)
			}
			if tc.bearer {
				req.Header.Set("Authorization", "Bearer synthetic")
			}
			if tc.cookie {
				req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: "synthetic"})
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			calls := 0
			handler := httpapi.CSRFMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want || (tc.want == 403 && calls != 0) {
				t.Fatalf("status=%d calls=%d", rec.Code, calls)
			}
		})
	}
}
