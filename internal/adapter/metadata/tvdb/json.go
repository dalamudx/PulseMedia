package tvdb

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !jsonValue(d, 0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
func jsonValue(d *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return false
			}
			name, valid := key.(string)
			if !valid {
				return false
			}
			name = strings.ToLower(name)
			if seen[name] {
				return false
			}
			seen[name] = true
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
	return e == nil && ((delimiter == '{' && end == json.Delim('}')) || (delimiter == '[' && end == json.Delim(']')))
}
