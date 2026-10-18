package crypto

import (
	"strings"
	"testing"
)

func TestPasswordHashRejectsMalformedParametersWithoutPanicking(t *testing.T) {
	hash, err := HashPassword("owned-auth-fixture")
	if err != nil {
		t.Fatal(err)
	}
	invalid := map[string]string{
		"version-trailing-data":   strings.Replace(hash, "v=19", "v=19junk", 1),
		"parameter-trailing-data": strings.Replace(hash, "p=4", "p=4junk", 1),
		"zero-iterations":         strings.Replace(hash, "t=1", "t=0", 1),
		"zero-lanes":              strings.Replace(hash, "p=4", "p=0", 1),
	}
	parts := strings.Split(hash, "$")
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for _, index := range []int{4, 5} {
		copyParts := append([]string(nil), parts...)
		value := copyParts[index]
		last := strings.IndexByte(alphabet, value[len(value)-1])
		copyParts[index] = value[:len(value)-1] + string(alphabet[last|1])
		invalid["noncanonical-base64-"+string(rune('0'+index))] = strings.Join(copyParts, "$")
	}
	invalid["overlong"] = strings.Repeat("x", 4096)
	for _, index := range []int{4, 5} {
		copyParts := append([]string(nil), parts...)
		copyParts[index] = "AA"
		invalid["invalid-length-"+string(rune('0'+index))] = strings.Join(copyParts, "$")
	}
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() != nil {
					t.Error("malformed stored hash panicked")
				}
			}()
			if VerifyPassword("owned-auth-fixture", value) {
				t.Error("malformed stored hash accepted")
			}
		})
	}
	if !VerifyPassword("owned-auth-fixture", hash) || VerifyPassword("different-fixture", hash) {
		t.Fatal("valid credential verification regressed")
	}
}
