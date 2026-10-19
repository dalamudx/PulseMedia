package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrictEndpointConfigurationJSON(t *testing.T) {
	const download = `{"name":"owned","client_type":"qbittorrent","url":"http://127.0.0.1:8080","username":"","password":""}`
	const media = `{"name":"owned","server_type":"jellyfin","url":"http://127.0.0.1:8096","api_key":""}`
	const version = `,"expected_updated_at":"2026-10-19T12:00:00Z"}`
	add := func(raw, fields string) string { return strings.TrimSuffix(raw, "}") + "," + fields + "}" }
	for _, family := range []struct{ name, body string }{{"download-clients", download}, {"media-servers", media}} {
		t.Run(family.name, func(t *testing.T) {
			mapping := "remote_path_map"
			if family.name == "media-servers" {
				mapping = "path_mapping"
			}
			edit := strings.TrimSuffix(family.body, "}") + version
			for _, tc := range []struct {
				name, method, suffix, body string
				valid                      bool
			}{
				{"create", http.MethodPost, "", family.body, true},
				{"edit", http.MethodPut, "/11111111-1111-4111-8111-111111111111", edit, true},
				{"mapping", http.MethodPost, "", add(family.body, `"`+mapping+`":{"/remote":"/local"}`), true},
				{"unknown", http.MethodPost, "", add(family.body, `"hidden":true`), false},
				{"duplicate", http.MethodPost, "", add(family.body, `"url":"http://elsewhere.invalid"`), false},
				{"escaped duplicate", http.MethodPost, "", add(family.body, `"\u0075rl":"http://elsewhere.invalid"`), false},
				{"wrong case", http.MethodPost, "", strings.Replace(family.body, `"name"`, `"Name"`, 1), false},
				{"null string", http.MethodPost, "", strings.Replace(family.body, `"name":"owned"`, `"name":null`, 1), false},
				{"null mapping", http.MethodPost, "", add(family.body, `"`+mapping+`":null`), false},
				{"nested duplicate", http.MethodPost, "", add(family.body, `"`+mapping+`":{"/remote":"/one","/remote":"/two"}`), false},
				{"trailing object", http.MethodPost, "", family.body + ` {}`, false},
				{"array", http.MethodPost, "", `[` + family.body + `]`, false},
				{"null", http.MethodPost, "", `null`, false},
				{"empty", http.MethodPost, "", ``, false},
				{"missing name", http.MethodPost, "", strings.Replace(family.body, `"name":"owned",`, ``, 1), false},
				{"missing version", http.MethodPut, "/11111111-1111-4111-8111-111111111111", family.body, false},
				{"bad version", http.MethodPut, "/11111111-1111-4111-8111-111111111111", strings.Replace(edit, "2026-10-19T12:00:00Z", "bad-date", 1), false},
				{"edit mapping", http.MethodPut, "/11111111-1111-4111-8111-111111111111", add(edit, `"`+mapping+`":{}`), false},
				{"oversize", http.MethodPost, "", strings.Replace(family.body, "owned", strings.Repeat("x", 128<<10), 1), false},
				{"probe untouched", http.MethodPost, "/11111111-1111-4111-8111-111111111111/test", ``, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					called := false
					h := strictEndpointConfigurationJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						called = true
						raw, err := io.ReadAll(r.Body)
						if err != nil || !bytes.Equal(raw, []byte(tc.body)) {
							t.Fatal("validated body was changed or unreadable")
						}
						w.WriteHeader(http.StatusNoContent)
					}))
					w := httptest.NewRecorder()
					h.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/"+family.name+tc.suffix, strings.NewReader(tc.body)))
					want := http.StatusBadRequest
					if tc.valid {
						want = http.StatusNoContent
					}
					if called != tc.valid || w.Code != want {
						t.Fatalf("called=%v status=%d, want valid=%v status=%d", called, w.Code, tc.valid, want)
					}
				})
			}
		})
	}
}
