package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestArgon2idPasswordHashing(t *testing.T) {
	password := "CorrectHorseBatteryStaple123!"

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	if !VerifyPassword(password, hash) {
		t.Fatal("expected password to match generated hash")
	}

	if VerifyPassword("WrongPassword123!", hash) {
		t.Fatal("expected wrong password to fail verification")
	}

	if VerifyPassword(password, "invalid_hash_format") {
		t.Fatal("expected malformed hash to fail verification")
	}
}

func TestXChaCha20Poly1305Encryption(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	plaintext := []byte("secret_api_token_value_to_encrypt")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext should not match plaintext")
	}

	decrypted, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("failed to decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("expected %s, got %s", plaintext, decrypted)
	}

	// Corrupted ciphertext should fail
	corrupted := append([]byte(nil), ciphertext...)
	corrupted[len(corrupted)-1] ^= 0xFF
	if _, err := Decrypt(key, corrupted); err == nil {
		t.Fatal("expected decryption of corrupted ciphertext to fail")
	}
}

func TestEnsureMasterKey(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pulsemedia-key-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	// 1. Generate key file on disk
	key1, err := EnsureMasterKey(tempDir, "")
	if err != nil {
		t.Fatalf("failed to create master key: %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(key1))
	}

	// Verify file mode 0600
	keyFile := filepath.Join(tempDir, "secret.key")
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("failed to stat secret.key: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected 0600 permissions, got %o", info.Mode().Perm())
	}

	// 2. Re-read existing key file
	key2, err := EnsureMasterKey(tempDir, "")
	if err != nil {
		t.Fatalf("failed to re-read master key: %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("expected re-read master key to match original")
	}

	// 3. Env var overrides file
	envKey, err := EnsureMasterKey(tempDir, "custom-env-secret-string-value")
	if err != nil {
		t.Fatalf("failed to derive from env secret: %v", err)
	}
	if bytes.Equal(envKey, key1) {
		t.Fatal("env secret should override file")
	}
}

func TestTokenGeneration(t *testing.T) {
	plain, hash, err := GenerateToken("pm_sess_")
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	if len(plain) <= 8 || plain[:8] != "pm_sess_" {
		t.Fatalf("unexpected plain token: %s", plain)
	}

	computedHash := HashToken(plain)
	if !bytes.Equal(hash, computedHash) {
		t.Fatal("computed hash should match generated hash")
	}
}
