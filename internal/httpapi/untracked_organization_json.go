package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
)

func untrackedOrganizationPath(parts []string) bool {
	return len(parts) >= 4 && parts[1] == "discoveries" && (parts[3] == "organizations" || parts[3] == "organization-commands")
}

// The scope guard checks the current Cookie actor before delegating here.
func validUntrackedOrganizationTransport(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if (len(parts) != 4 && len(parts) != 5) || !untrackedOrganizationPath(parts) ||
		!canonicalUntrackedReviewID(parts[0]) || !canonicalUntrackedReviewID(parts[2]) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return false
	}
	keys := r.Header.Values("Idempotency-Key")
	if r.Method == http.MethodGet && len(parts) == 5 {
		if len(keys) != 0 || (parts[3] == "organizations" && !canonicalUntrackedReviewID(parts[4])) ||
			(parts[3] == "organization-commands" && !validDoubanSyncCommandKey(parts[4])) {
			return false
		}
		if r.Body != nil {
			defer func() { _ = r.Body.Close() }()
			body, err := io.ReadAll(io.LimitReader(r.Body, 1))
			if err != nil || len(body) != 0 {
				return false
			}
		}
		return true
	}
	if r.Method != http.MethodPost || len(parts) != 4 || parts[3] != "organizations" ||
		len(keys) != 1 || !validDoubanSyncCommandKey(keys[0]) || r.Body == nil {
		return false
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		return false
	}
	original := r.Body
	defer func() { _ = original.Close() }()
	raw, err := io.ReadAll(http.MaxBytesReader(w, original, 4095))
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if !uniqueEndpointJSONValue(decoder, 0, map[string]bool{"confirmation_id": true}) {
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	var input struct {
		ConfirmationID string `json:"confirmation_id"`
	}
	if json.Unmarshal(raw, &input) != nil || !canonicalUntrackedReviewID(input.ConfirmationID) {
		return false
	}
	// The generated strict binder receives the exact validated bytes.
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	return true
}
