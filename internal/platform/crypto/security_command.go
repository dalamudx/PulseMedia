package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (c *SettingsCipher) CommandDigest(value []byte) string {
	h := hmac.New(sha256.New, c.key)
	_, _ = h.Write([]byte("pulse.security.command.v1\x00"))
	_, _ = h.Write(value)
	return hex.EncodeToString(h.Sum(nil))
}

var _ ports.SecurityCipher = (*SettingsCipher)(nil)
