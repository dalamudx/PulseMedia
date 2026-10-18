package ports

// IndexerExecutionModes freezes protocol and legacy behavior independently of URL shape.
type IndexerExecutionModes struct {
	Protocol IndexerProtocol
	LegacyV1 bool
}

// LegacyIndexerProtocolClassifier is the application's pure, exact legacy classifier.
// Inputs contain secrets and must never be logged or retained.
type LegacyIndexerProtocolClassifier func(rawURL, credential string) IndexerProtocol
