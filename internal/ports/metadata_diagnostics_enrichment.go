package ports

const (
	DiagnosticFanartAPI MetadataDiagnosticTarget = "fanart_api"
	DiagnosticTVDBAPI   MetadataDiagnosticTarget = "tvdb_api"
)

func (t MetadataDiagnosticTarget) Valid() bool {
	switch t {
	case DiagnosticTMDBAPI, DiagnosticTMDBImage, DiagnosticFanartAPI, DiagnosticTVDBAPI:
		return true
	}
	return false
}
