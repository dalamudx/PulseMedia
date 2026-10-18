package ports

import (
	"context"
	"github.com/google/uuid"
)

// A bounded persistent observation, never a file observation or execution permit.
type CompleteTVOrganizationVideoPreview struct {
	UnitID         uuid.UUID `json:"unit_id"`
	UnitKey        string    `json:"unit_key"`
	SourceRelative string    `json:"source_relative"`
	TargetRelative string    `json:"target_relative"`
	SizeBytes      int64     `json:"size_bytes"`
}
type CompleteTVOrganizationPreview struct {
	CompleteTVOrganizationReferences
	Season            int                                  `json:"season"`
	Videos            []CompleteTVOrganizationVideoPreview `json:"videos"`
	AttachmentCount   int                                  `json:"attachment_count"`
	OrganizationState string                               `json:"organization_state"`
	CanConfirm        bool                                 `json:"can_confirm"`
}
type CompleteTVOrganizationPreviewPage struct {
	Items []CompleteTVOrganizationPreview `json:"items"`
}
type CompleteTVOrganizationPreviewRepository interface {
	ReadCompleteTVOrganizations(context.Context, *Actor, uuid.UUID) (*CompleteTVOrganizationPreviewPage, error)
}
