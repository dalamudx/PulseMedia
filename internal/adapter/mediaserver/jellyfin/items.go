package jellyfin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const maxItemsBody = 8 << 20

func findItem(resp *http.Response, path string) (string, bool, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxItemsBody+1))
	if err != nil || len(body) > maxItemsBody {
		return "", false, ports.ErrBadResponse
	}
	var proof struct {
		Items json.RawMessage `json:"Items"`
		Total *int64          `json:"TotalRecordCount"`
	}
	if json.Unmarshal(body, &proof) != nil || len(proof.Items) == 0 || string(proof.Items) == "null" {
		return "", false, ports.ErrBadResponse
	}
	var items []struct {
		ID   string `json:"Id"`
		Path string `json:"Path"`
	}
	if json.Unmarshal(proof.Items, &items) != nil || proof.Total == nil || *proof.Total != int64(len(items)) {
		return "", false, ports.ErrBadResponse
	}
	match := ""
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" || item.Path == "" || seen[item.ID] {
			return "", false, ports.ErrBadResponse
		}
		seen[item.ID] = true
		if item.Path != path {
			continue
		}
		if match != "" {
			return "", false, ports.ErrBadResponse
		}
		match = item.ID
	}
	return match, match != "", nil
}
