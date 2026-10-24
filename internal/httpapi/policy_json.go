package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// strictPolicyJSON runs inside authentication, before generated binding loses
// duplicate keys or accepts case-insensitive aliases of explicit field names.
func strictPolicyJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const root = "/api/v1/policies"
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		var body any
		var fields map[string]bool
		switch {
		case r.URL.Path == root:
			body = &CreatePolicyJSONRequestBody{}
			fields = map[string]bool{"id": true, "name": true, "rules": true}
		case r.URL.Path == root+"/preview":
			body = &PreviewPolicyJSONRequestBody{}
			fields = map[string]bool{"policy_id": false, "revision": false, "rules": false, "release_title": true}
		case strings.HasPrefix(r.URL.Path, root+"/") && strings.HasSuffix(r.URL.Path, "/revisions") && strings.Count(strings.TrimPrefix(r.URL.Path, root+"/"), "/") == 1:
			body = &PublishPolicyRevisionJSONRequestBody{}
			fields = map[string]bool{"expected_revision": true, "rules": false, "restore_revision": false}
		default:
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid quality policy request", GetRequestID(r.Context()))
		}
		if r.Body == nil {
			fail()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 128<<10))
		if err != nil {
			fail()
			return
		}
		tokens := json.NewDecoder(bytes.NewReader(raw))
		tokens.UseNumber()
		if !uniqueEndpointJSONValue(tokens, 0, fields) {
			fail()
			return
		}
		if _, err = tokens.Token(); err != io.EOF {
			fail()
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(body) != nil || !policyWordJSONShape(raw) {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}

func policyWordJSONShape(raw []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	rulesRaw, present := root["rules"]
	if !present {
		return true
	}
	var rules map[string]json.RawMessage
	if json.Unmarshal(rulesRaw, &rules) != nil {
		return false
	}
	for _, key := range []string{"cutoff_score", "prefer_hdr", "resolution_order", "source_order", "reject_terms", "reject_groups"} {
		if _, present := rules[key]; !present {
			return false
		}
	}
	for key := range rules {
		switch key {
		case "cutoff_score", "prefer_hdr", "resolution_order", "source_order", "reject_terms", "reject_groups", "custom_words":
		default:
			return false
		}
	}
	wordsRaw, present := rules["custom_words"]
	if !present {
		return true
	}
	var words []map[string]json.RawMessage
	if json.Unmarshal(wordsRaw, &words) != nil || len(words) > 64 {
		return false
	}
	for _, word := range words {
		if len(word) != 4 {
			return false
		}
		for _, key := range []string{"word", "score", "required", "excluded"} {
			if _, present := word[key]; !present {
				return false
			}
		}
	}
	return true
}
