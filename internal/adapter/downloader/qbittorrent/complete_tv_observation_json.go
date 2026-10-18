package qbittorrent

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// The standard JSON struct decoder silently takes the last duplicate identity.
// Reject duplicates (including case aliases) before any identity is interpreted.
func uniqueCompleteTVObservationObject(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		member, e := d.Token()
		if e != nil {
			return false
		}
		key, ok := member.(string)
		if !ok {
			return false
		}
		// Native field names are ASCII. Reject Unicode aliases before the struct
		// decoder's broader Unicode folding can overwrite canonical evidence.
		for _, r := range key {
			if r > 0x7f {
				return false
			}
		}
		key = strings.ToLower(key)
		if seen[key] {
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return false
		}
	}
	last, err := d.Token()
	if err != nil || last != json.Delim('}') {
		return false
	}
	var trailing any
	return d.Decode(&trailing) == io.EOF
}
