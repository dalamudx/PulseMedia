package auth

import "github.com/dalamudx/PulseMedia/internal/ports"

// SetMFACipher is called during assembly before HTTP is opened. Enabled MFA fails closed without it.
func (s *Service) SetMFACipher(cipher ports.SettingsCipher) { s.mfaCipher = cipher }
