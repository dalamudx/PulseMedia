// Package tvdb defines explicit, optional metadata fallback configuration.
package tvdb

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const CredentialsSetting = "tvdb.credentials"
const FallbackSetting = "tvdb.fallback"

var ErrInvalidConfiguration = errors.New("invalid TVDB configuration")

type Credentials struct {
	APIKey string `json:"api_key"`
	PIN    string `json:"pin,omitempty"`
}

func ValidCredential(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	for _, ch := range value {
		if ch < '!' || ch > '~' {
			return false
		}
	}
	return true
}
func DecodeCredentials(raw []byte) (Credentials, error) {
	var zero Credentials
	if len(raw) > 2048 {
		return zero, ErrInvalidConfiguration
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, e := d.Token(); e != nil || token != json.Delim('{') {
		return zero, ErrInvalidConfiguration
	}
	values := map[string]string{}
	for d.More() {
		token, e := d.Token()
		if e != nil {
			return zero, ErrInvalidConfiguration
		}
		key, ok := token.(string)
		if !ok || (key != "api_key" && key != "pin") {
			return zero, ErrInvalidConfiguration
		}
		if _, exists := values[key]; exists {
			return zero, ErrInvalidConfiguration
		}
		var value *string
		if d.Decode(&value) != nil || value == nil {
			return zero, ErrInvalidConfiguration
		}
		values[key] = *value
	}
	if token, e := d.Token(); e != nil || token != json.Delim('}') {
		return zero, ErrInvalidConfiguration
	}
	if _, e := d.Token(); e != io.EOF {
		return zero, ErrInvalidConfiguration
	}
	if !ValidCredential(values["api_key"]) || (values["pin"] != "" && !ValidCredential(values["pin"])) {
		return zero, ErrInvalidConfiguration
	}
	return Credentials{APIKey: values["api_key"], PIN: values["pin"]}, nil
}
func DecodeEnabled(raw []byte) (bool, error) {
	if len(raw) > 128 {
		return false, ErrInvalidConfiguration
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, e := d.Token(); e != nil || token != json.Delim('{') {
		return false, ErrInvalidConfiguration
	}
	if token, e := d.Token(); e != nil || token != "enabled" {
		return false, ErrInvalidConfiguration
	}
	var enabled *bool
	if d.Decode(&enabled) != nil || enabled == nil {
		return false, ErrInvalidConfiguration
	}
	if token, e := d.Token(); e != nil || token != json.Delim('}') {
		return false, ErrInvalidConfiguration
	}
	if _, e := d.Token(); e != io.EOF {
		return false, ErrInvalidConfiguration
	}
	return *enabled, nil
}
