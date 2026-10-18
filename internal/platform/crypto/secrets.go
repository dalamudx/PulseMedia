package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// SecretProtector uses the existing system cipher and a keyed request digest.
// Key material is injected and copied; no files or environment are accessed here.
type SecretProtector struct{ key []byte }

func NewSecretProtector(key []byte) *SecretProtector {
	return &SecretProtector{append([]byte(nil), key...)}
}
func (s *SecretProtector) EncryptSecret(plain []byte) ([]byte, error) { return Encrypt(s.key, plain) }
func (s *SecretProtector) DecryptSecret(ciphertext []byte) ([]byte, error) {
	return Decrypt(s.key, ciphertext)
}
func (s *SecretProtector) DigestSecret(plain []byte) (string, error) {
	if len(s.key) != 32 {
		return "", errors.New("invalid system key")
	}
	m := hmac.New(sha256.New, s.key)
	m.Write(plain)
	return hex.EncodeToString(m.Sum(nil)), nil
}
