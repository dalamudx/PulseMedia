package ports

import "context"

// MetadataDiagnosticTarget is a server-owned catalogue, never an outbound URL.
type MetadataDiagnosticTarget string

const (
	DiagnosticTMDBAPI   MetadataDiagnosticTarget = "tmdb_api"
	DiagnosticTMDBImage MetadataDiagnosticTarget = "tmdb_image"
)

type MetadataDiagnosticResult struct {
	Target         MetadataDiagnosticTarget `json:"target"`
	Category       string                   `json:"category"`
	ElapsedMS      int64                    `json:"elapsed_ms"`
	HostResponded  bool                     `json:"host_responded"`
	ImageRetrieved bool                     `json:"image_retrieved"`
}
type MetadataDiagnosticSource interface {
	OpenDiagnostic(context.Context) (MetadataDiagnosticSession, error)
}
type MetadataDiagnosticSession interface {
	Validate(context.Context) error
	Probe(context.Context, MetadataDiagnosticTarget) MetadataDiagnosticResult
}
