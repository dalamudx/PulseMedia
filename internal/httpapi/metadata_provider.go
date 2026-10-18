package httpapi

import "github.com/dalamudx/PulseMedia/internal/ports"

// SetMetadataProvider injects the metadata gateway at application assembly.
func (s *ServerHandler) SetMetadataProvider(provider ports.MetadataProvider) {
	s.metaProvider = provider
}
