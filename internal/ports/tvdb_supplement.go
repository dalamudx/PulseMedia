package ports

// TVDBSeasonSupplement is an additive observation, never a complete canonical season.
// The caller must persist source evidence atomically before exposing new units.
// Unmatched source IDs do not carry download or canonical mapping permission.
type TVDBSeasonSupplement struct {
	TMDBSeriesID      int64
	TVDBSeriesID      int64
	CredentialVersion int64
	FallbackVersion   int64
	Metadata          TVSeasonMetadata
	Added             []TVDBEpisodeResolution
	UnmatchedTVDBIDs  []int64
}
