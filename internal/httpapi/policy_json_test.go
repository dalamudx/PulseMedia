package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrictPolicyJSON(t *testing.T) {
	const rules = `{"cutoff_score":400,"prefer_hdr":false,"resolution_order":{"1080p":300},"source_order":{"HDTV":10},"reject_terms":[],"reject_groups":[],"custom_words":[{"word":"DV","score":100,"required":false,"excluded":false}]}`
	for _, family := range []struct{ name, path, body string }{
		{"create", "/api/v1/policies", `{"id":"owned","name":"Owned","rules":` + rules + `}`},
		{"publish", "/api/v1/policies/owned/revisions", `{"expected_revision":1,"rules":` + rules + `}`},
		{"preview", "/api/v1/policies/preview", `{"release_title":"Movie.DV","rules":` + rules + `}`},
	} {
		t.Run(family.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, body string
				valid      bool
			}{
				{"valid", family.body, true},
				{"duplicate_word", strings.Replace(family.body, `"word":"DV"`, `"word":"DV","word":"Atmos"`, 1), false},
				{"escaped_duplicate", strings.Replace(family.body, `"word":"DV"`, `"word":"DV","w\u006frd":"Atmos"`, 1), false},
				{"duplicate_score", strings.Replace(family.body, `"score":100`, `"score":100,"score":0`, 1), false},
				{"duplicate_weight", strings.Replace(family.body, `"1080p":300`, `"1080p":300,"1080p":0`, 1), false},
				{"unknown_word_field", strings.Replace(family.body, `"word":"DV"`, `"word":"DV","execute":true`, 1), false},
				{"word_case_alias", strings.Replace(family.body, `"word":"DV"`, `"Word":"DV"`, 1), false},
				{"rule_case_alias", strings.Replace(family.body, `"prefer_hdr"`, `"Prefer_Hdr"`, 1), false},
				{"missing_word_flag", strings.Replace(family.body, `,"excluded":false`, ``, 1), false},
				{"missing_rule_field", strings.Replace(family.body, `"prefer_hdr":false,`, ``, 1), false},
				{"two_values", family.body + `{}`, false},
				{"null", `null`, false},
				{"array", `[]`, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					called := false
					handler := strictPolicyJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						called = true
						raw, err := io.ReadAll(r.Body)
						if err != nil || !bytes.Equal(raw, []byte(tc.body)) {
							t.Fatal("valid body was changed")
						}
						w.WriteHeader(http.StatusNoContent)
					}))
					out := httptest.NewRecorder()
					handler.ServeHTTP(out, httptest.NewRequest(http.MethodPost, family.path, strings.NewReader(tc.body)))
					want := http.StatusBadRequest
					if tc.valid {
						want = http.StatusNoContent
					}
					if out.Code != want || called != tc.valid || out.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatalf("status=%d called=%t valid=%t", out.Code, called, tc.valid)
					}
				})
			}
		})
	}
}
