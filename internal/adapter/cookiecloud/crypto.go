package cookiecloud

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	CryptoLegacy            = "legacy"
	CryptoFixed             = "aes-128-cbc-fixed"
	maxEncryptedBackupBytes = 8 << 20
)

// DecryptBackup implements the two encryption formats in CookieCloud's official
// api/app.js. The legacy MD5 derivation is required by that wire protocol; it is
// not the application's secret-storage scheme. Passwords never leave this call.
func DecryptBackup(encrypted, cryptoType, userUUID, password string) ([]byte, error) {
	if userUUID == "" || password == "" || len(userUUID) > 4096 || len(password) > 4096 || !utf8.ValidString(userUUID) || !utf8.ValidString(password) {
		return nil, ports.ErrRejected
	}
	if cryptoType == "" {
		cryptoType = CryptoLegacy
	}
	if cryptoType != CryptoLegacy && cryptoType != CryptoFixed {
		return nil, ports.ErrBadResponse
	}
	if encrypted == "" || len(encrypted) > maxEncryptedBackupBytes || strings.ContainsAny(encrypted, " \t\r\n") {
		return nil, ports.ErrBadResponse
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encrypted)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	key := DeriveKey(userUUID, password)
	defer clear(key)
	var plain []byte
	if cryptoType == CryptoLegacy {
		plain, err = decryptLegacy(raw, key)
	} else {
		plain, err = decryptCBC(raw, key, make([]byte, aes.BlockSize))
	}
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(plain) {
		clear(plain)
		return nil, ports.ErrBadResponse
	}
	return plain, nil
}

func decryptLegacy(raw, passphrase []byte) ([]byte, error) {
	if len(passphrase) != 16 || len(raw) < 2*aes.BlockSize || len(raw)%aes.BlockSize != 0 || string(raw[:8]) != "Salted__" {
		return nil, ports.ErrBadResponse
	}
	// CryptoJS uses EVP_BytesToKey with MD5 and one iteration, deriving a
	// 32-byte AES key and a separate 16-byte IV from the eight-byte salt.
	material := make([]byte, 0, 48)
	var previous []byte
	for len(material) < 48 {
		h := md5.New()
		_, _ = h.Write(previous)
		_, _ = h.Write(passphrase)
		_, _ = h.Write(raw[8:16])
		previous = h.Sum(nil)
		material = append(material, previous...)
	}
	defer clear(material)
	defer clear(previous)
	return decryptCBC(raw[16:], material[:32], material[32:48])
}

func decryptCBC(raw, key, iv []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw)%aes.BlockSize != 0 || len(iv) != aes.BlockSize {
		return nil, ports.ErrBadResponse
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ports.ErrBadResponse
	}
	plain := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, raw)
	n := int(plain[len(plain)-1])
	if n < 1 || n > aes.BlockSize || n > len(plain) {
		clear(plain)
		return nil, ports.ErrBadResponse
	}
	for _, value := range plain[len(plain)-n:] {
		if value != byte(n) {
			clear(plain)
			return nil, ports.ErrBadResponse
		}
	}
	clear(plain[len(plain)-n:])
	return plain[:len(plain)-n], nil
}
