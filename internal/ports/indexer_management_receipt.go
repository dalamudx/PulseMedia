package ports

import "context"

// IndexerManagementReceiptReader returns safe original command receipts after
// current account authorization. It never replays a write or returns secrets.
type IndexerManagementReceiptReader interface {
	ReadIndexerManagementReceipt(context.Context, *Actor, IndexerMutationAction, string) (*IndexerManagementReceipt, error)
}
