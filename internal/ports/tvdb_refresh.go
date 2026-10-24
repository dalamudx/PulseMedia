package ports

import "context"

// TVSupplementRefreshRepository adds independent provenance to the original refresh UoW.
// Ordinary complete snapshots and historical command receipts keep their contract.
type TVSupplementRefreshRepository interface {
	TVRefreshRepository
	ApplyTVRefreshWithSupplements(context.Context, *Actor, TVRefreshContext, TVRefreshCommand, []TVSeasonMetadata, []TVDBSeasonSupplement, string, string) (*TVRefreshReceipt, error)
}

type TVSeasonSupplementProvider interface {
	ReadSeasonSupplement(context.Context, int64, int, *TVSeasonMetadata) (*TVDBSeasonSupplement, error)
}
