package ports

import "context"

// IndexerCapabilities is observed provider evidence, not execution authorization.
type IndexerCapabilities struct {
	DefaultLimit, MaxLimit        int32 // Zero means no limit was advertised.
	Search, MovieSearch, TVSearch IndexerSearchCapability
}
type IndexerSearchCapability struct {
	Available       bool
	SupportedParams []string
}
type IndexerCapabilitiesProvider interface {
	Capabilities(context.Context) (IndexerCapabilities, error)
}
