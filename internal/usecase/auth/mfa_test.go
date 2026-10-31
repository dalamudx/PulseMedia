package auth

import (
	"encoding/base32"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"testing"
	"time"
)

func TestTOTPStandardVectorsAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range []struct {
		seconds int64
		code    string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		got := totpCode(secret, v.seconds/30)
		if got != v.code {
			t.Fatalf("RFC6238 at %d got %s want %s", v.seconds, got, v.code)
		}
		counter, ok := totpCounter(secret, v.code, time.Unix(v.seconds, 0), -1)
		if !ok || counter != v.seconds/30 {
			t.Fatal("valid code rejected")
		}
		if _, ok = totpCounter(secret, v.code, time.Unix(v.seconds, 0), counter); ok {
			t.Fatal("counter replay accepted")
		}
	}
	now := time.Unix(1234567890, 0)
	for _, bad := range []string{"", "12345", "1234567", "abcdef", " 05924"} {
		if _, ok := totpCounter(secret, bad, now, -1); ok {
			t.Fatal("malformed OTP accepted")
		}
	}
	if _, ok := totpCounter(secret, totpCode(secret, now.Unix()/30-2), now, -1); ok {
		t.Fatal("out of window OTP accepted")
	}
}
func TestSecurityCommandInputBounds(t *testing.T) {
	if validUsername(" ab") || validUsername("a\nname") || validUsername("ab") || !validUsername("家人一号") {
		t.Fatal("username validation")
	}
	if validPassword("short") || !validPassword("Password123") {
		t.Fatal("password validation")
	}
	if validProof(ports.MFAProof{OTP: "123456", RecoveryCode: "recovery"}) || validProof(ports.MFAProof{OTP: "1234567"}) {
		t.Fatal("ambiguous factor accepted")
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	if encoded != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Fatal("unexpected enrollment encoding")
	}
}
