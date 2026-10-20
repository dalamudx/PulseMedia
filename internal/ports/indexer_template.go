package ports

// IndexerCategoryMappingView represents a category code and its label in a template.
type IndexerCategoryMappingView struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// IndexerTemplateView is the safe public representation of an indexer preset.
type IndexerTemplateView struct {
	ID                    string                       `json:"id"`
	Name                  string                       `json:"name"`
	Domain                string                       `json:"domain"`
	Protocol              IndexerProtocol              `json:"protocol"`
	DefaultURL            string                       `json:"default_url"`
	RecommendedCategories []int                        `json:"recommended_categories"`
	CategoryMappings      []IndexerCategoryMappingView `json:"category_mappings"`
	Description           string                       `json:"description"`
	CredentialHelp        string                       `json:"credential_help"`
}
