package httpapi

// SetLibraryMaintenanceService injects explicit same-root maintenance operations.
func (s *ServerHandler) SetLibraryMaintenanceService(v LibraryMaintenanceApplication) {
	s.libraryMaintenanceSvc = v
}
