package ports

import "github.com/google/uuid"

// ScanRootSnapshot is the configuration and health epoch actually probed.
type ScanRootSnapshot struct {
	ID                                      uuid.UUID
	Path, MediaType, MarkerFilename, Health string
	Generation, HealthRevision              int64
	// RootIdentity is the physical root bound by a completed scan in this generation.
	RootIdentity *FileIdentity
}

// ScanRunSnapshot freezes the starting content watermark and root token.
type ScanRunSnapshot struct {
	Generation, ScanToken, StartMutationSeq int64
}
