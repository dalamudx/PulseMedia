package artwork

import (
	"strings"
	"testing"
)

func TestArtworkConfigurationRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `true`, `{"enabled":null}`, `{"enabled":"true"}`, `{"Enabled":true}`, `{"enabled":true,"enabled":false}`, `{"enabled":true,"api_key":"secret"}`, `{"enabled":true} {}`} {
		if _, err := DecodeSwitch([]byte(raw)); err == nil {
			t.Fatalf("invalid switch accepted: %s", raw)
		}
	}
	for _, v := range []bool{false, true} {
		raw := `{"enabled":false}`
		if v {
			raw = `{"enabled":true}`
		}
		got, err := DecodeSwitch([]byte(raw))
		if err != nil || got != v {
			t.Fatal(got, err)
		}
	}
	for _, key := range []string{"", " key", "key ", "a\nb", "a\x00b", strings.Repeat("x", 513)} {
		if ValidAPIKey(key) {
			t.Fatal("invalid key accepted")
		}
	}
	if !ValidAPIKey("fixture-key") || !ValidAPIKey(strings.Repeat("x", 512)) {
		t.Fatal("valid key rejected")
	}
	for _, raw := range []string{`{}`, `{"api_key":"plain"}`, `{"ciphertext":""}`, `{"ciphertext":"!!!"}`, `{"ciphertext":"YQ==","ciphertext":"Yg=="}`, `{"ciphertext":"YQ==","api_key":"plain"}`} {
		if ValidCiphertext([]byte(raw)) {
			t.Fatal("plaintext or malformed envelope accepted")
		}
	}
	if !ValidCiphertext([]byte(`{"ciphertext":"YQ=="}`)) {
		t.Fatal("opaque ciphertext envelope rejected")
	}
}
