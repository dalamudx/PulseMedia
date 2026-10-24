package emby

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// Reject ambiguous objects at every depth, including case aliases accepted by encoding/json.
func validJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !jsonValue(d, 0) {
		return false
	}
	_, e := d.Token()
	return e == io.EOF
}
func jsonValue(d *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	t, e := d.Token()
	if e != nil {
		return false
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return false
			}
			s, ok := key.(string)
			if !ok {
				return false
			}
			s = strings.ToLower(s)
			if seen[s] {
				return false
			}
			seen[s] = true
			if !jsonValue(d, depth+1) {
				return false
			}
		}
	case '[':
		for d.More() {
			if !jsonValue(d, depth+1) {
				return false
			}
		}
	default:
		return false
	}
	end, e := d.Token()
	return e == nil && (delim == '{' && end == json.Delim('}') || delim == '[' && end == json.Delim(']'))
}
