package crypto

import "github.com/dalamudx/PulseMedia/internal/ports"

type SettingsCipher struct{ key []byte }

func NewSettingsCipher(key []byte) *SettingsCipher {
	return &SettingsCipher{key: append([]byte(nil), key...)}
}
func (c *SettingsCipher) Encrypt(value []byte) ([]byte, error) { return Encrypt(c.key, value) }
func (c *SettingsCipher) Decrypt(value []byte) ([]byte, error) { return Decrypt(c.key, value) }

var _ ports.SettingsCipher = (*SettingsCipher)(nil)
