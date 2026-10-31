package httpapi

// SetExternalBatchService injects immutable previews with independent row authority.
func (s *ServerHandler) SetExternalBatchService(v ExternalBatchApplication) {
	s.externalBatchSvc = v
}
