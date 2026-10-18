package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

var (
	ErrInvalidHash = errors.New("invalid password hash format")
	ErrDecrypt     = errors.New("failed to decrypt ciphertext")
)

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // 64MB
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword hashes a plaintext password using Argon2id.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// Format: $argon2id$v=19$m=65536,t=1,p=4$<salt-b64>$<hash-b64>
	encodedSalt := base64.RawStdEncoding.EncodeToString(salt)
	encodedHash := base64.RawStdEncoding.EncodeToString(hash)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, encodedSalt, encodedHash), nil
}

// VerifyPassword checks if a plaintext password matches an Argon2id hash.
func VerifyPassword(password, encodedHash string) bool {
	// Only the immutable profile emitted by HashPassword is supported. Stored
	// strings cannot select work factors or arbitrarily sized derived keys.
	if len(encodedHash) > 128 {
		return false
	}
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	if parts[2] != fmt.Sprintf("v=%d", argon2.Version) || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", argonMemory, argonTime, argonThreads) {
		return false
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != saltLen {
		return false
	}

	expectedHash, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(expectedHash) != argonKeyLen {
		return false
	}

	actualHash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(expectedHash, actualHash) == 1
}

// Encrypt encrypts plaintext with XChaCha20-Poly1305.
func Encrypt(key []byte, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize AEAD cipher: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Output: nonce + ciphertext + tag
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt decrypts ciphertext using XChaCha20-Poly1305.
func Decrypt(key []byte, ciphertext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize AEAD cipher: %w", err)
	}

	nonceSize := aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, ErrDecrypt
	}

	nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := aead.Open(nil, nonce, actualCiphertext, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// EnsureMasterKey returns or initializes the 32-byte master key for secret encryption.
func EnsureMasterKey(dataDir string, envSecret string) ([]byte, error) {
	if envSecret != "" {
		// Try hex decode first
		if decoded, err := hex.DecodeString(envSecret); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
		// Try raw 32 bytes or hash it deterministically
		if len(envSecret) == 32 {
			return []byte(envSecret), nil
		}
		hash := sha256.Sum256([]byte(envSecret))
		return hash[:], nil
	}

	if dataDir == "" {
		return nil, errors.New("data directory cannot be empty")
	}

	return loadOrCreateMasterKey(dataDir)
}

// GenerateToken generates a random plaintext token and its SHA-256 hash.
func GenerateToken(prefix string) (plain string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", nil, fmt.Errorf("failed to generate random token: %w", err)
	}

	plain = prefix + hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(plain))
	return plain, sum[:], nil
}

// HashToken computes the SHA-256 hash of a plaintext token.
func HashToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}
