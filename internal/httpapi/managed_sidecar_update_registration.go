package httpapi

// SetManagedSidecarUpdateService injects explicit, provenance-checked updates.
func (s *ServerHandler) SetManagedSidecarUpdateService(v ManagedSidecarUpdateApplication) {
	s.managedSidecarUpdateSvc = v
}
