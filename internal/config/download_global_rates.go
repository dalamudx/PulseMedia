package config

// DownloadGlobalRatesConfig opts into background native statistics collection.
// It never changes per-task completion evidence or downloader rate limits.
type DownloadGlobalRatesConfig struct {
	Enabled bool `yaml:"enabled"`
}
