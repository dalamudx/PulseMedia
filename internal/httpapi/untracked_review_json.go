package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func canonicalUntrackedReviewID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func validUntrackedReviewVersion(value int64) bool {
	return value >= 1 && value < math.MaxInt64
}

func validUntrackedReviewInput(in ports.UntrackedReviewInput) bool {
	return validUntrackedReviewVersion(in.ScopeVersion) && validUntrackedReviewVersion(in.FileRevision) &&
		(in.MediaType == "movie" || in.MediaType == "tv") && in.TMDBID >= 0
}

func validUntrackedReviewHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validUntrackedReviewCandidateKey(value string) bool {
	suffix, ok := strings.CutPrefix(value, "tmdb.movie:")
	if !ok {
		suffix, ok = strings.CutPrefix(value, "tmdb.tv:")
	}
	id, err := strconv.ParseInt(suffix, 10, 64)
	return ok && err == nil && id > 0 && strconv.FormatInt(id, 10) == suffix
}

func validUntrackedConfirmationInput(in ports.UntrackedConfirmationInput) bool {
	if in.ReviewID == uuid.Nil || !validUntrackedReviewHash(in.MaterialHash) ||
		!validUntrackedReviewVersion(in.ScopeVersion) || !validUntrackedReviewVersion(in.FileRevision) ||
		!validUntrackedReviewCandidateKey(in.CandidateKey) {
		return false
	}
	target := in.TargetRelativePath
	if target == "" || target == "." || len(target) > 4096 || path.IsAbs(target) || path.Clean(target) != target ||
		target == ".." || strings.HasPrefix(target, "../") || strings.ContainsAny(target, "\\\x00\r\n\t") || !naming.IsVideoPath(target) {
		return false
	}
	switch in.Action {
	case "hardlink", "copy", "reflink", "move":
		return true
	default:
		return false
	}
}

// The parent guard authenticates before delegating nested discovery routes here.
func validUntrackedReviewTransport(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if (len(parts) != 4 && len(parts) != 5) || parts[1] != "discoveries" ||
		!canonicalUntrackedReviewID(parts[0]) || !canonicalUntrackedReviewID(parts[2]) ||
		r.URL.RawQuery != "" || r.URL.ForceQuery {
		return false
	}
	post := len(parts) == 4 && (parts[3] == "reviews" || parts[3] == "confirmations")
	read := len(parts) == 5 && ((parts[3] == "reviews" && canonicalUntrackedReviewID(parts[4])) ||
		((parts[3] == "review-commands" || parts[3] == "confirmation-commands") && validDoubanSyncCommandKey(parts[4])))
	keys := r.Header.Values("Idempotency-Key")
	if read && r.Method == http.MethodGet {
		if len(keys) != 0 {
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
	if !post || r.Method != http.MethodPost || len(keys) != 1 || !validDoubanSyncCommandKey(keys[0]) || r.Body == nil {
		return false
	}
	original := r.Body
	defer func() { _ = original.Close() }()
	raw, err := io.ReadAll(http.MaxBytesReader(w, original, 4095))
	if err != nil {
		return false
	}
	fields := map[string]bool{"scope_version": true, "file_revision": true, "media_type": true, "tmdb_id": true}
	if parts[3] == "confirmations" {
		fields = map[string]bool{"review_id": true, "material_hash": true, "scope_version": true, "file_revision": true, "candidate_key": true, "action": true, "target_relative_path": true}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !uniqueEndpointJSONValue(decoder, 0, fields) {
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if parts[3] == "reviews" {
		var input ports.UntrackedReviewInput
		if decoder.Decode(&input) != nil || !validUntrackedReviewInput(input) {
			return false
		}
	} else {
		var input ports.UntrackedConfirmationInput
		var id struct {
			ReviewID string `json:"review_id"`
		}
		if decoder.Decode(&input) != nil || !validUntrackedConfirmationInput(input) ||
			json.Unmarshal(raw, &id) != nil || !canonicalUntrackedReviewID(id.ReviewID) {
			return false
		}
	}
	// Preserve the exact validated bytes for the generated strict decoder.
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	return true
}
