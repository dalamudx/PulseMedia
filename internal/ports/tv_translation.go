package ports

import "context"

// TVTranslation is an explicitly enumerated locale, not a guess from script or title equality.
// Empty fields mean absent only after a complete, validated translations response.
type TVTranslation struct {
	TMDBID          int64
	Language        string
	Title, Overview string
}
type TVTranslationProvider interface {
	GetTVTranslation(context.Context, int64, string) (*TVTranslation, error)
}
type TVTranslationSource struct {
	Provider string   `json:"provider"`
	SourceID int64    `json:"source_id"`
	Language string   `json:"language"`
	Fields   []string `json:"fields"`
}
