package library

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
	"testing"
)

type externalBatchFake struct {
	batch                         ports.ExternalBatch
	confirmKeys, organizationKeys []string
	confirmedInput                []ports.UntrackedConfirmationInput
	confirmation                  *ports.UntrackedConfirmationReceipt
	confirmError                  error
	creates                       int
}

func (f *externalBatchFake) CreateExternalBatch(context.Context, *ports.Actor, uuid.UUID, ports.ExternalBatchInput, string) (*ports.ExternalBatch, error) {
	f.creates++
	return &f.batch, nil
}
func (f *externalBatchFake) ReadExternalBatch(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (*ports.ExternalBatch, error) {
	return &f.batch, nil
}
func (f *externalBatchFake) Confirm(_ context.Context, _ *ports.Actor, _, _ uuid.UUID, in ports.UntrackedConfirmationInput, key string) (*ports.UntrackedConfirmationReceipt, error) {
	f.confirmKeys = append(f.confirmKeys, key)
	f.confirmedInput = append(f.confirmedInput, in)
	return f.confirmation, f.confirmError
}
func (f *externalBatchFake) ConfirmationReceipt(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (*ports.UntrackedConfirmationReceipt, error) {
	if f.confirmation == nil {
		return nil, ports.ErrNotFound
	}
	return f.confirmation, nil
}
func (f *externalBatchFake) Organize(_ context.Context, _ *ports.Actor, _, _ uuid.UUID, _ ports.UntrackedOrganizationInput, key string) (*ports.UntrackedOrganizationReceipt, error) {
	f.organizationKeys = append(f.organizationKeys, key)
	return &ports.UntrackedOrganizationReceipt{}, nil
}
func (f *externalBatchFake) Receipt(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, string) (*ports.UntrackedOrganizationReceipt, error) {
	return nil, ports.ErrNotFound
}
func (f *externalBatchFake) Read(context.Context, *ports.Actor, uuid.UUID, uuid.UUID, uuid.UUID) (*ports.UntrackedOrganizationView, error) {
	return nil, ports.ErrNotFound
}
func externalBatchServiceFixture() (*ports.Actor, *externalBatchFake, *ExternalBatchService) {
	a := &ports.Actor{ID: uuid.NewString(), Type: ports.ActorTypeUser, Role: ports.RoleAdmin}
	f := &externalBatchFake{batch: ports.ExternalBatch{ID: uuid.New(), ActorID: uuid.MustParse(a.ID), ScopeID: uuid.New(), CommandKey: "batch-original"}}
	for i := range 2 {
		target := "Series/A.mkv"
		if i == 1 {
			target = "Series/B.mkv"
		}
		f.batch.Rows = append(f.batch.Rows, ports.ExternalBatchRow{Selection: ports.ExternalBatchSelection{FileID: uuid.New(), Confirmation: ports.UntrackedConfirmationInput{ReviewID: uuid.New(), MaterialHash: strings.Repeat("a", 64), ScopeVersion: 1, FileRevision: 2, CandidateKey: "tmdb.tv:123", Action: "hardlink", TargetRelativePath: target}}})
	}
	return a, f, NewExternalBatchService(f, f, f)
}
func TestExternalBatchReadNeverSendsAndRowsRetainFrozenAuthority(t *testing.T) {
	a, f, s := externalBatchServiceFixture()
	ctx := t.Context()
	view, e := s.Read(ctx, a, f.batch.ScopeID, f.batch.ID, "")
	if e != nil || len(view.Status) != 2 {
		t.Fatal(view, e)
	}
	if len(f.confirmKeys)+len(f.organizationKeys) != 0 {
		t.Fatal("GET sent a command")
	}
	f.confirmError = ports.ErrUntrackedReviewStale
	_, e = s.Confirm(ctx, a, f.batch.ScopeID, f.batch.ID, 0)
	if !errors.Is(e, ports.ErrUntrackedReviewStale) {
		t.Fatal(e)
	}
	f.confirmError = nil
	f.confirmation = &ports.UntrackedConfirmationReceipt{Material: ports.UntrackedConfirmationMaterial{ID: uuid.New(), Input: f.batch.Rows[1].Selection.Confirmation}}
	if _, e = s.Confirm(ctx, a, f.batch.ScopeID, f.batch.ID, 1); e != nil {
		t.Fatal(e)
	}
	if f.confirmedInput[0] != f.batch.Rows[0].Selection.Confirmation || f.confirmedInput[1] != f.batch.Rows[1].Selection.Confirmation || f.confirmKeys[0] == f.confirmKeys[1] {
		t.Fatal("rows shared or re-signed authority")
	}
	if _, e = s.Organize(ctx, a, f.batch.ScopeID, f.batch.ID, 1); e != nil {
		t.Fatal(e)
	}
	key := f.organizationKeys[0]
	if _, e = s.Organize(ctx, a, f.batch.ScopeID, f.batch.ID, 1); e != nil || len(f.organizationKeys) != 2 || f.organizationKeys[1] != key {
		t.Fatal("explicit retry changed original key", e)
	}
	f.confirmation.Material.Input = f.batch.Rows[0].Selection.Confirmation
	if _, e = s.Organize(ctx, a, f.batch.ScopeID, f.batch.ID, 1); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("another row confirmation accepted", e)
	}
}
func TestExternalBatchInputAndOwnershipFences(t *testing.T) {
	for _, variant := range []string{"empty", "over-limit", "file", "target", "account", "row"} {
		t.Run(variant, func(t *testing.T) {
			a, f, s := externalBatchServiceFixture()
			in := ports.ExternalBatchInput{Rows: []ports.ExternalBatchSelection{f.batch.Rows[0].Selection, f.batch.Rows[1].Selection}}
			switch variant {
			case "empty":
				in.Rows = nil
			case "over-limit":
				in.Rows = make([]ports.ExternalBatchSelection, 21)
			case "file":
				in.Rows[1].FileID = in.Rows[0].FileID
			case "target":
				in.Rows[1].Confirmation.TargetRelativePath = in.Rows[0].Confirmation.TargetRelativePath
			case "account":
				a.Role = ports.RoleMember
			case "row":
				_, e := s.Confirm(t.Context(), a, f.batch.ScopeID, f.batch.ID, 2)
				if !errors.Is(e, ports.ErrRejected) {
					t.Fatal(e)
				}
				return
			}
			if _, e := s.Create(t.Context(), a, f.batch.ScopeID, in, "original"); e == nil {
				t.Fatal("invalid create")
			}
			if f.creates != 0 {
				t.Fatal("invalid request reached repository")
			}
		})
	}
	a, f, s := externalBatchServiceFixture()
	f.batch.ActorID = uuid.New()
	if _, e := s.Read(t.Context(), a, f.batch.ScopeID, f.batch.ID, ""); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("another account manifest accepted", e)
	}
}
