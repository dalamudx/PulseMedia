package ports

// IndexerProtocol is selected explicitly by the administrator.
type IndexerProtocol string

const (
	IndexerProtocolTorznab  IndexerProtocol = "torznab"
	IndexerProtocolNexusPHP IndexerProtocol = "nexusphp"
	IndexerProtocolRSS      IndexerProtocol = "rss"
)

// IndexerConfigurationInput contains secrets and must never be logged.
// Categories are positive Torznab-compatible signed 32-bit IDs.
type IndexerConfigurationInput struct {
	Protocol   IndexerProtocol
	URL        string `json:"-"`
	Credential string `json:"-"`
	Categories []int
}

// PreparedIndexerConfiguration carries separate system-envelope ciphertexts
// for persistence. DisplayURL contains only the origin, without userinfo.
type PreparedIndexerConfiguration struct {
	Protocol             IndexerProtocol
	DisplayURL           string
	URLCiphertext        []byte `json:"-"`
	CredentialCiphertext []byte `json:"-"`
	Categories           []int
}
