package download

import "github.com/dalamudx/PulseMedia/internal/ports"

// SetIndexerConfigurationDecoder is assembled before starting download workers.
func (r *Repository) SetIndexerConfigurationDecoder(decoder ports.CredentialDecoder) {
	r.execution.SetIndexerConfigurationDecoder(decoder)
}

func (m *Manager) SetIndexerConfigurationDecoder(decoder ports.CredentialDecoder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.indexerDecoder = decoder
}

func (m *Manager) sourceConfigurationDecoder() ports.CredentialDecoder {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.indexerDecoder
}
