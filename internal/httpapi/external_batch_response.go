package httpapi

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"time"
)

type externalBatchPublicRow struct {
	Selection ports.ExternalBatchSelection  `json:"selection"`
	Scope     UntrackedScope                `json:"scope"`
	File      UntrackedDiscoveryObservation `json:"file"`
	Review    UntrackedReviewView           `json:"review"`
}
type externalBatchPublicManifest struct {
	ID               uuid.UUID                `json:"id"`
	ActorID          uuid.UUID                `json:"actor_id"`
	ScopeID          uuid.UUID                `json:"scope_id"`
	CommandKey       string                   `json:"command_key"`
	CreatedAt        time.Time                `json:"created_at"`
	Rows             []externalBatchPublicRow `json:"rows"`
	ExecutionAllowed bool                     `json:"execution_allowed"`
}
type externalBatchPublicStatus struct {
	Confirmation *UntrackedConfirmationReceipt `json:"confirmation"`
	Organization *UntrackedOrganizationReceipt `json:"organization"`
	Progress     *UntrackedOrganizationView    `json:"progress"`
}
type externalBatchPublicView struct {
	Batch  externalBatchPublicManifest `json:"batch"`
	Status []externalBatchPublicStatus `json:"status"`
}

// Stored manifests keep their immutable representation; transport uses the
// same public objects as the independent file review and organization routes.
func externalBatchViewResponse(v *ports.ExternalBatchView) externalBatchPublicView {
	out := externalBatchPublicView{Status: []externalBatchPublicStatus{}}
	if v == nil {
		return out
	}
	b := v.Batch
	out.Batch = externalBatchPublicManifest{ID: b.ID, ActorID: b.ActorID, ScopeID: b.ScopeID, CommandKey: b.CommandKey, CreatedAt: b.CreatedAt, Rows: make([]externalBatchPublicRow, 0, len(b.Rows)), ExecutionAllowed: false}
	for _, r := range b.Rows {
		f := r.File
		out.Batch.Rows = append(out.Batch.Rows, externalBatchPublicRow{Selection: r.Selection, Scope: untrackedScopeResponse(r.Scope), File: UntrackedDiscoveryObservation{Id: f.ID, ScopeId: f.ScopeID, ScopeVersion: f.ScopeVersion, Revision: f.Revision, RelativePath: f.RelativePath, Device: f.Identity.Device, Inode: f.Identity.Inode, Size: f.Identity.Size, Mtime: f.Identity.Mtime, FirstObservedAt: f.FirstObservedAt, LastObservedAt: f.LastObservedAt, Observations: f.Observations}, Review: untrackedReviewResponse(r.Review)})
	}
	for _, s := range v.Status {
		var row externalBatchPublicStatus
		if s.Confirmation != nil {
			x := untrackedConfirmationReceiptResponse(*s.Confirmation)
			row.Confirmation = &x
		}
		if s.Organization != nil {
			x := untrackedOrganizationReceiptResponse(*s.Organization)
			row.Organization = &x
		}
		if s.Progress != nil {
			x := untrackedOrganizationViewResponse(*s.Progress)
			row.Progress = &x
		}
		out.Status = append(out.Status, row)
	}
	return out
}
