package crypto

import "github.com/dalamudx/PulseMedia/internal/ports"

// AuthenticationSecrets adapts the canonical system primitives to authentication.
// It keeps Argon2 validation and token hashes identical across existing sessions.
type AuthenticationSecrets struct{}

var _ ports.AuthenticationSecrets = AuthenticationSecrets{}

func (AuthenticationSecrets) HashPassword(password string) (string, error) {
	return HashPassword(password)
}
func (AuthenticationSecrets) VerifyPassword(password, encodedHash string) bool {
	return VerifyPassword(password, encodedHash)
}
func (AuthenticationSecrets) GenerateToken(prefix string) (string, []byte, error) {
	return GenerateToken(prefix)
}
func (AuthenticationSecrets) HashToken(token string) []byte {
	return HashToken(token)
}
