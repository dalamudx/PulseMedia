package ports

import "context"

// Recovery only requeues the original R0 submission job when no preparation,
// task or live physical call exists. Prepared work is handed to observation;
// lease expiry alone never authorizes another Add or releases protection.
type AutomaticTVSubmissionRecoveryRepository interface {
	RecoverAutomaticTVSubmissions(context.Context, int32) (int, error)
}
