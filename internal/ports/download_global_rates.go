package ports

import "context"

// DownloadGlobalRates contains instantaneous daemon-wide byte rates, including
// transfers that PulseMedia does not track. Nil means unknown, not zero.
// Callers bind observation time and source configuration outside the adapter.
type DownloadGlobalRates struct {
	DownloadBytesPerSecond *int64
	UploadBytesPerSecond   *int64
}

// DownloadGlobalRatesReader is a read-only capability separate from task evidence.
type DownloadGlobalRatesReader interface {
	ReadGlobalRates(ctx context.Context) (DownloadGlobalRates, error)
}
