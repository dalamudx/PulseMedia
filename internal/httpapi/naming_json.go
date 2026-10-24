package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
)

func namingNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSuffix(r.URL.Path, "/") == "/api/v1/organization/naming/preview" || strings.TrimSuffix(r.URL.Path, "/") == "/api/v1/settings/"+naming.TemplateSettingKey || strings.HasPrefix(r.URL.Path, "/api/v1/settings/"+naming.TemplateSettingKey+"/commands/") {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func strictNamingJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		preview := r.Method == http.MethodPost && r.URL.Path == "/api/v1/organization/naming/preview"
		save := r.Method == http.MethodPut && r.URL.Path == "/api/v1/settings/"+naming.TemplateSettingKey
		if !preview && !save {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid naming request", GetRequestID(r.Context()))
		}
		if r.Body == nil {
			fail()
			return
		}
		original := r.Body
		defer func() { _ = original.Close() }()
		raw, err := io.ReadAll(http.MaxBytesReader(w, original, 32<<10))
		if err != nil {
			fail()
			return
		}
		fields := map[string]bool{"value": true, "expected_version": true}
		if preview {
			fields = map[string]bool{"media_type": true, "title": true, "extension": true, "year": false, "season": false, "episode": false, "tmdb_id": false, "imdb_id": false, "release_title": false, "templates": false}
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if !uniqueEndpointJSONValue(decoder, 0, fields) {
			fail()
			return
		}
		if _, err = decoder.Token(); err != io.EOF {
			fail()
			return
		}
		var root map[string]json.RawMessage
		if json.Unmarshal(raw, &root) != nil {
			fail()
			return
		}
		for key, value := range root {
			if _, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				fail()
				return
			}
		}
		templateKey := "value"
		if preview {
			templateKey = "templates"
		}
		if value, present := root[templateKey]; present {
			var declaration map[string]json.RawMessage
			if json.Unmarshal(value, &declaration) != nil || len(declaration) != 2 || declaration["movie_template"] == nil || declaration["episode_template"] == nil {
				fail()
				return
			}
			for _, field := range declaration {
				var text *string
				if json.Unmarshal(field, &text) != nil || text == nil {
					fail()
					return
				}
			}
			if preview {
				if _, err := naming.DecodeTemplateRules(value); err != nil {
					fail()
					return
				}
			}
		}
		decoder = json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var body any = &UpdateSettingJSONRequestBody{}
		if preview {
			body = &PreviewNamingJSONRequestBody{}
		}
		if decoder.Decode(body) != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}
