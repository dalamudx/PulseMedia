package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Strict endpoint bodies must be single objects with exact field names. The
// token walk rejects duplicates at every depth before typed decoding can lose them.
func strictEndpointConfigurationJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		var fields map[string]bool
		for _, family := range []string{"download-clients", "media-servers"} {
			root := "/api/v1/" + family
			create := r.Method == http.MethodPost && r.URL.Path == root
			edit := r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, root+"/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, root+"/"), "/")
			if !create && !edit {
				continue
			}
			if family == "download-clients" {
				fields = map[string]bool{"name": true, "client_type": true, "url": true, "username": true, "password": true}
				if create {
					body = &CreateDownloadClientJSONRequestBody{}
					fields["remote_path_map"] = false
				} else {
					body = &UpdateDownloadClientJSONRequestBody{}
				}
			} else {
				fields = map[string]bool{"name": true, "server_type": true, "url": true, "api_key": true}
				if create {
					body = &CreateMediaServerJSONRequestBody{}
					fields["path_mapping"] = false
				} else {
					body = &UpdateMediaServerJSONRequestBody{}
				}
			}
			if edit {
				fields["expected_updated_at"] = true
			}
			break
		}
		if body == nil {
			next.ServeHTTP(w, r)
			return
		}
		fail := func() {
			WriteJSONError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid endpoint configuration request", GetRequestID(r.Context()))
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
		if _, err := tokens.Token(); err != io.EOF {
			fail()
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(body) != nil {
			fail()
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}

func uniqueEndpointJSONValue(d *json.Decoder, depth int, required map[string]bool) bool {
	if depth > 32 {
		return false
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return false
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return required == nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			token, err := d.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			if required != nil {
				if _, allowed := required[key]; !allowed {
					return false
				}
			}
			seen[key] = true
			if !uniqueEndpointJSONValue(d, depth+1, nil) {
				return false
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return false
		}
		for key, mandatory := range required {
			if mandatory && !seen[key] {
				return false
			}
		}
		return true
	case '[':
		if required != nil {
			return false
		}
		for d.More() {
			if !uniqueEndpointJSONValue(d, depth+1, nil) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}
