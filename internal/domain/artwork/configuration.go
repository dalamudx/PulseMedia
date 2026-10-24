// Package artwork defines the finite configuration for optional sidecar artwork.
package artwork

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const APIKeySetting = "fanart.api_key"
const SwitchSetting = "fanart.artwork"

var ErrInvalidConfiguration = errors.New("invalid artwork configuration")

func ValidAPIKey(key string) bool {
	if key == "" || len(key) > 512 || !utf8.ValidString(key) || strings.TrimSpace(key) != key {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// DecodeSwitch accepts exactly one boolean field. Null, duplicate keys and
// unknown fields cannot turn a malformed configuration into a valid disable.
func DecodeSwitch(raw []byte) (bool, error) {
	if len(raw) > 128 {
		return false, ErrInvalidConfiguration
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if tok, e := d.Token(); e != nil || tok != json.Delim('{') {
		return false, ErrInvalidConfiguration
	}
	if tok, e := d.Token(); e != nil || tok != "enabled" {
		return false, ErrInvalidConfiguration
	}
	var enabled *bool
	if e := d.Decode(&enabled); e != nil || enabled == nil {
		return false, ErrInvalidConfiguration
	}
	if tok, e := d.Token(); e != nil || tok != json.Delim('}') {
		return false, ErrInvalidConfiguration
	}
	if _, e := d.Token(); e != io.EOF {
		return false, ErrInvalidConfiguration
	}
	return *enabled, nil
}

func ValidCiphertext(raw []byte) bool {
	if len(raw) > 16384 {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if tok, e := d.Token(); e != nil || tok != json.Delim('{') {
		return false
	}
	if tok, e := d.Token(); e != nil || tok != "ciphertext" {
		return false
	}
	var ciphertext string
	if d.Decode(&ciphertext) != nil {
		return false
	}
	if tok, e := d.Token(); e != nil || tok != json.Delim('}') {
		return false
	}
	if _, e := d.Token(); e != io.EOF {
		return false
	}
	decoded, e := base64.StdEncoding.Strict().DecodeString(ciphertext)
	return e == nil && len(decoded) > 0
}
