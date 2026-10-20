package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoubanSyncConfigStrictJSON(t *testing.T) {
	valid := `{"douban_uid":"exherb","enabled":false,"expected_version":0}`
	tests := []struct{ name, body string }{
		{"empty", ""}, {"null", "null"}, {"array", "[]"}, {"string", `"x"`}, {"bool", "true"}, {"number", "1"},
		{"empty-object", "{}"}, {"truncated", `{"douban_uid":`}, {"trailing-object", valid + `{}`}, {"trailing-null", valid + `null`}, {"trailing-junk", valid + `x`},
		{"missing-uid", `{"enabled":false,"expected_version":0}`},
		{"missing-enabled", `{"douban_uid":"exherb","expected_version":0}`},
		{"missing-version", `{"douban_uid":"exherb","enabled":false}`},
		{"uid-number", strings.Replace(valid, `"exherb"`, `123`, 1)},
		{"enabled-string", strings.Replace(valid, `false`, `"false"`, 1)},
		{"enabled-number", strings.Replace(valid, `false`, `0`, 1)},
		{"version-string", strings.Replace(valid, `:0`, `:"0"`, 1)},
		{"version-bool", strings.Replace(valid, `:0`, `:false`, 1)},
		{"version-cannot-increment", strings.Replace(valid, `:0`, `:9223372036854775807`, 1)},
		{"negative", strings.Replace(valid, `:0`, `:-1`, 1)}, {"overflow", strings.Replace(valid, `:0`, `:9223372036854775808`, 1)},
		{"float", strings.Replace(valid, `:0`, `:0.0`, 1)}, {"exponent", strings.Replace(valid, `:0`, `:1e0`, 1)},
		{"unknown", strings.TrimSuffix(valid, "}") + `,"owner_id":"x"}`},
		{"nested-unknown", strings.TrimSuffix(valid, "}") + `,"config":{}}`},
		{"uid-empty", strings.Replace(valid, `"exherb"`, `""`, 1)}, {"uid-leading-dot", strings.Replace(valid, `"exherb"`, `".abc"`, 1)},
		{"uid-space", strings.Replace(valid, `"exherb"`, `"a b"`, 1)}, {"uid-url", strings.Replace(valid, `"exherb"`, `"https://x"`, 1)},
		{"uid-unicode", strings.Replace(valid, `"exherb"`, `"用户"`, 1)}, {"uid-long", strings.Replace(valid, `"exherb"`, `"`+strings.Repeat("a", 65)+`"`, 1)},
		{"size-4096", valid + strings.Repeat(" ", 4096-len(valid))}, {"size-4097", valid + strings.Repeat(" ", 4097-len(valid))},
		{"bad-escape", strings.Replace(valid, "exherb", `\q`, 1)}, {"comment", valid + "//x"}, {"trailing-comma", strings.TrimSuffix(valid, "}") + ",}"},
	}
	for _, field := range []string{"douban_uid", "enabled", "expected_version"} {
		for _, value := range []string{"null", "[]", "{}"} {
			var body string
			switch field {
			case "douban_uid":
				body = strings.Replace(valid, `"exherb"`, value, 1)
			case "enabled":
				body = strings.Replace(valid, "false", value, 1)
			default:
				body = strings.Replace(valid, `:0`, ":"+value, 1)
			}
			tests = append(tests, struct{ name, body string }{field + "-" + value, body})
		}
		tests = append(tests, struct{ name, body string }{field + "-alias", strings.Replace(valid, field, strings.ToUpper(field), 1)})
		tests = append(tests, struct{ name, body string }{field + "-duplicate", strings.TrimSuffix(valid, "}") + fmt.Sprintf(`,"%s":null}`, field)})
	}
	for _, field := range []string{"id", "owner_id", "version", "created_at", "updated_at"} {
		tests = append(tests, struct{ name, body string }{"server-" + field, strings.TrimSuffix(valid, "}") + fmt.Sprintf(`,"%s":"server"}`, field)})
	}
	for _, field := range []string{"movie_root_id", "tv_root_id"} {
		for _, value := range []string{"null", `"bad"`, `"00000000-0000-0000-0000-000000000000"`, "true", "1", "[]", "{}"} {
			tests = append(tests, struct{ name, body string }{field + value, strings.TrimSuffix(valid, "}") + fmt.Sprintf(`,"%s":%s}`, field, value)})
		}
	}
	tests = append(tests, struct{ name, body string }{"escaped-duplicate", strings.TrimSuffix(valid, "}") + `,"douban_\u0075id":"other"}`})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			h := strictDoubanSyncConfigJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			r := httptest.NewRequest("PUT", doubanSyncConfigPath, strings.NewReader(tt.body))
			r.Header.Set("Idempotency-Key", "key")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if called || w.Code != 400 || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("called=%v status=%d", called, w.Code)
			}
		})
	}
	t.Logf("%d rejected body cases", len(tests))
	for _, body := range []string{valid, strings.Replace(valid, `"exherb"`, `"a-B_2.c"`, 1), strings.TrimSuffix(valid, "}") + `,"movie_root_id":"11111111-1111-4111-8111-111111111111"}`} {
		called := false
		h := strictDoubanSyncConfigJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), `"enabled":false`) {
				t.Fatal("false lost")
			}
			w.WriteHeader(204)
		}))
		r := httptest.NewRequest("PUT", doubanSyncConfigPath, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", "key:1._-")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !called || w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestDoubanSyncConfigStrictEnvelope(t *testing.T) {
	for _, key := range []string{"", "a,b", "a b", "é", strings.Repeat("a", 161), "a/b", "a\tb"} {
		t.Run("key-"+key, func(t *testing.T) {
			h := strictDoubanSyncConfigJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("called") }))
			r := httptest.NewRequest("PUT", doubanSyncConfigPath, strings.NewReader(`{"douban_uid":"a","enabled":false,"expected_version":0}`))
			r.Header.Set("Idempotency-Key", key)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal(w.Code)
			}
		})
	}
	for _, tt := range []struct {
		name, method, path, body string
		headers                  []string
	}{
		{"multi-header", "PUT", doubanSyncConfigPath, `{"douban_uid":"a","enabled":false,"expected_version":0}`, []string{"a", "b"}},
		{"put-query", "PUT", doubanSyncConfigPath + "?owner=x", "{}", []string{"a"}},
		{"current-query", "GET", doubanSyncConfigPath + "?x=y", "", nil},
		{"receipt-query", "GET", doubanSyncConfigPath + "/commands/key?command_key=other", "", nil},
		{"current-body", "GET", doubanSyncConfigPath, "{}", nil},
		{"receipt-body", "GET", doubanSyncConfigPath + "/commands/key", "{}", nil},
		{"bad-path-key", "GET", doubanSyncConfigPath + "/commands/a,b", "", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := strictDoubanSyncConfigJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("called") }))
			r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			for _, v := range tt.headers {
				r.Header.Add("Idempotency-Key", v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal(w.Code)
			}
		})
	}
	// The old POST and unrelated routes pass through untouched.
	for _, path := range []string{"/api/v1/sync/douban", "/unrelated"} {
		called := false
		h := strictDoubanSyncConfigJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(503) }))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader("{}")))
		if !called || w.Code != 503 {
			t.Fatal("legacy route changed")
		}
	}
}
