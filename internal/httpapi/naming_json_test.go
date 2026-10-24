package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrictNamingJSON(t *testing.T) {
	const templates = `{"movie_template":"{title}.{extension}","episode_template":"{title}/{episode_key}.{extension}"}`
	for _, family := range []struct{ name, method, path, body string }{
		{"save", http.MethodPut, "/api/v1/settings/organization.naming_templates", `{"value":` + templates + `,"expected_version":0}`},
		{"preview", http.MethodPost, "/api/v1/organization/naming/preview", `{"media_type":"movie","title":"Fixture","extension":"mkv","templates":` + templates + `}`},
	} {
		t.Run(family.name, func(t *testing.T) {
			for _, test := range []struct {
				name, body string
				valid      bool
			}{
				{"valid", family.body, true},
				{"duplicate", strings.Replace(family.body, `"movie_template":`, `"movie_template":"","movie_template":`, 1), false},
				{"escaped_duplicate", strings.Replace(family.body, `"movie_template":`, `"movi\u0065_template":"","movie_template":`, 1), false},
				{"case_alias", strings.Replace(family.body, `"movie_template"`, `"Movie_template"`, 1), false},
				{"missing_field", strings.Replace(family.body, `,"episode_template":"{title}/{episode_key}.{extension}"`, "", 1), false},
				{"null_field", strings.Replace(family.body, `"episode_template":"{title}/{episode_key}.{extension}"`, `"episode_template":null`, 1), false},
				{"unknown_field", strings.Replace(family.body, `"movie_template":`, `"execute":true,"movie_template":`, 1), false},
				{"root_unknown", strings.Replace(family.body, `{`, `{"execute":true,`, 1), false},
				{"root_null", strings.Replace(family.body, templates, `null`, 1), false},
				{"two_values", family.body + `{}`, false},
				{"null", `null`, false}, {"array", `[]`, false},
			} {
				t.Run(test.name, func(t *testing.T) {
					called := false
					handler := strictNamingJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						called = true
						raw, err := io.ReadAll(r.Body)
						if err != nil || !bytes.Equal(raw, []byte(test.body)) {
							t.Fatal("valid request bytes changed")
						}
						w.WriteHeader(http.StatusNoContent)
					}))
					out := httptest.NewRecorder()
					handler.ServeHTTP(out, httptest.NewRequest(family.method, family.path, strings.NewReader(test.body)))
					expected := http.StatusBadRequest
					if test.valid {
						expected = http.StatusNoContent
					}
					if out.Code != expected || called != test.valid || out.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal("naming strict boundary failed", out.Code, called)
					}
				})
			}
		})
	}
}

func TestNamingNoStoreIncludesErrors(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		for _, path := range []string{"/api/v1/organization/naming/preview", "/api/v1/settings/organization.naming_templates", "/api/v1/settings/organization.naming_templates/commands/original"} {
			out := httptest.NewRecorder()
			namingNoStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })).ServeHTTP(out, httptest.NewRequest(http.MethodPost, path, nil))
			if out.Code != status || out.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("private naming response could be cached", status)
			}
		}
	}
}
