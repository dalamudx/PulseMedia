package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type untrackedOrganizationTestCapture struct {
	material ports.UntrackedOrganizationMaterial
}

func (c *untrackedOrganizationTestCapture) UntrackedOrganizationMaterial() ports.UntrackedOrganizationMaterial {
	return c.material
}

type untrackedOrganizationTestRepo struct {
	t                                  *testing.T
	actor                              ports.Actor
	scopeID, fileID                    uuid.UUID
	input                              ports.UntrackedOrganizationInput
	key                                string
	capture                            *untrackedOrganizationTestCapture
	preparation                        *ports.UntrackedOrganizationPreparation
	receipt                            *ports.UntrackedOrganizationReceipt
	view                               *ports.UntrackedOrganizationView
	prepares, commits, receipts, reads int
	err, commitErr                     error
	onPrepare                          func()
	onCommit                           func(context.Context, *ports.UntrackedOrganizationReceipt)
}

func (r *untrackedOrganizationTestRepo) check(a *ports.Actor, scopeID, fileID uuid.UUID) {
	r.t.Helper()
	if a == nil || !reflect.DeepEqual(*a, r.actor) || scopeID != r.scopeID || fileID != r.fileID {
		r.t.Error("repository received another actor or object")
	}
}

func (r *untrackedOrganizationTestRepo) PrepareUntrackedOrganization(_ context.Context, a *ports.Actor, scopeID, fileID uuid.UUID, in ports.UntrackedOrganizationInput, key string) (*ports.UntrackedOrganizationPreparation, error) {
	r.prepares++
	r.check(a, scopeID, fileID)
	if in != r.input || key != r.key {
		r.t.Error("prepare changed the requested confirmation or original key")
	}
	if r.onPrepare != nil {
		r.onPrepare()
	}
	return r.preparation, r.err
}

func (r *untrackedOrganizationTestRepo) CommitUntrackedOrganization(ctx context.Context, c ports.UntrackedOrganizationCapture) (*ports.UntrackedOrganizationReceipt, error) {
	r.commits++
	if c != r.capture {
		r.t.Error("commit replaced the same-instance opaque capture")
	}
	out := &ports.UntrackedOrganizationReceipt{CommandKey: r.key, Operation: r.capture.material.Operation, State: "queued"}
	if r.onCommit != nil {
		r.onCommit(ctx, out)
	}
	return out, r.commitErr
}

func (r *untrackedOrganizationTestRepo) ReadUntrackedOrganizationReceipt(_ context.Context, a *ports.Actor, scopeID, fileID uuid.UUID, key string) (*ports.UntrackedOrganizationReceipt, error) {
	r.receipts++
	r.check(a, scopeID, fileID)
	if key != r.key {
		r.t.Error("receipt lookup changed the original key")
	}
	return r.receipt, r.err
}

func (r *untrackedOrganizationTestRepo) ReadUntrackedOrganization(_ context.Context, a *ports.Actor, scopeID, fileID, operationID uuid.UUID) (*ports.UntrackedOrganizationView, error) {
	r.reads++
	r.check(a, scopeID, fileID)
	if operationID != r.capture.material.Operation.ID {
		r.t.Error("view lookup changed operation ID")
	}
	return r.view, r.err
}

type untrackedOrganizationTestFixture struct {
	base    *untrackedReviewTestFixture
	repo    *untrackedOrganizationTestRepo
	service *UntrackedOrganizationService
}

func untrackedOrganizationFixture(t *testing.T) *untrackedOrganizationTestFixture {
	t.Helper()
	base := untrackedReviewFixture()
	c := *base.repo.confirmation
	c.Material = untrackedReviewMaterialCopy(c.Material)
	o := ports.UntrackedOrganizationOperation{ID: uuid.New(), ActorID: c.Material.ActorID, ScopeID: c.Material.Scope.ID, FileID: c.Material.File.ID, ConfirmationID: c.Material.ID, PlanID: uuid.New(), StepID: uuid.New(), JobID: uuid.New(), ScopeVersion: c.Material.Scope.Version, FileRevision: c.Material.File.Revision, MaterialHash: c.Material.Input.MaterialHash, CreatedAt: c.Material.CreatedAt.Add(time.Second)}
	capture := &untrackedOrganizationTestCapture{material: ports.UntrackedOrganizationMaterial{Operation: o, Confirmation: c}}
	o.MaterialHash = untrackedOrganizationMaterialHash(capture.material)
	capture.material.Operation = o
	r := &untrackedOrganizationTestRepo{t: t, actor: *base.actor, scopeID: o.ScopeID, fileID: o.FileID, input: ports.UntrackedOrganizationInput{ConfirmationID: o.ConfirmationID}, key: "organize-1", capture: capture, preparation: &ports.UntrackedOrganizationPreparation{Capture: capture}, receipt: &ports.UntrackedOrganizationReceipt{CommandKey: "organize-1", Operation: o, State: "queued"}, view: &ports.UntrackedOrganizationView{Operation: o, State: "queued"}}
	return &untrackedOrganizationTestFixture{base: base, repo: r, service: NewUntrackedOrganizationService(r, base.files)}
}

func (f *untrackedOrganizationTestFixture) resealMaterial() {
	f.repo.capture.material.Operation.MaterialHash = untrackedOrganizationMaterialHash(f.repo.capture.material)
	f.repo.receipt.Operation = f.repo.capture.material.Operation
	f.repo.view.Operation = f.repo.capture.material.Operation
}

func (f *untrackedOrganizationTestFixture) organize(ctx context.Context) (*ports.UntrackedOrganizationReceipt, error) {
	return f.service.Organize(ctx, f.base.actor, f.repo.scopeID, f.repo.fileID, f.repo.input, f.repo.key)
}

func (f *untrackedOrganizationTestFixture) receipt(ctx context.Context) (*ports.UntrackedOrganizationReceipt, error) {
	return f.service.Receipt(ctx, f.base.actor, f.repo.scopeID, f.repo.fileID, f.repo.key)
}

func (f *untrackedOrganizationTestFixture) read(ctx context.Context) (*ports.UntrackedOrganizationView, error) {
	return f.service.Read(ctx, f.base.actor, f.repo.scopeID, f.repo.fileID, f.repo.capture.material.Operation.ID)
}

func (f *untrackedOrganizationTestFixture) assertNoMetadata(t *testing.T) {
	t.Helper()
	m := f.base.metadata
	if m.searches != 0 || m.movies != 0 || m.tvs != 0 || m.seasons != 0 {
		t.Fatal("organization called metadata")
	}
}

func TestUntrackedOrganizationOriginalKeyOffline(t *testing.T) {
	f := untrackedOrganizationFixture(t)
	f.repo.receipt.Operation.CreatedAt = f.repo.receipt.Operation.CreatedAt.In(time.FixedZone("original", 9*3600))
	f.repo.preparation = &ports.UntrackedOrganizationPreparation{Receipt: f.repo.receipt}
	f.base.files.identities = nil
	f.service.files = nil
	before, err := json.Marshal(f.repo.receipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []func(context.Context) (*ports.UntrackedOrganizationReceipt, error){f.organize, f.receipt} {
		out, err := call(t.Context())
		if err != nil || out == nil || out.State != "queued" || out.ExecutionAllowed {
			t.Fatal("original queued receipt unavailable offline", err)
		}
		after, err := json.Marshal(out)
		if err != nil || string(before) != string(after) {
			t.Fatal("historical receipt bytes changed", err)
		}
		out.CommandKey = "caller mutation"
	}
	if f.repo.prepares != 1 || f.repo.receipts != 1 || f.repo.commits != 0 || f.base.files.opens.Load() != 0 || f.base.files.locks.Load() != 0 {
		t.Fatal("original-key recovery did work beyond the repository lookup")
	}
	f.assertNoMetadata(t)
}

func TestUntrackedOrganizationActorAndCommandBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*untrackedOrganizationTestFixture)
		want   error
	}{
		{"anonymous", func(f *untrackedOrganizationTestFixture) { f.base.actor = nil }, ports.ErrUnauthorized},
		{"zero actor", func(f *untrackedOrganizationTestFixture) { f.base.actor.ID = uuid.Nil.String() }, ports.ErrForbidden},
		{"noncanonical actor", func(f *untrackedOrganizationTestFixture) { f.base.actor.ID = "urn:uuid:" + f.base.actor.ID }, ports.ErrForbidden},
		{"member", func(f *untrackedOrganizationTestFixture) { f.base.actor.Role = ports.RoleMember }, ports.ErrForbidden},
		{"system", func(f *untrackedOrganizationTestFixture) { f.base.actor.Type = ports.ActorTypeSystem }, ports.ErrForbidden},
		{"empty token scopes", func(f *untrackedOrganizationTestFixture) { f.base.actor.Scopes = []string{} }, ports.ErrForbidden},
		{"token scopes", func(f *untrackedOrganizationTestFixture) { f.base.actor.Scopes = []string{"*"} }, ports.ErrForbidden},
		{"scope", func(f *untrackedOrganizationTestFixture) { f.repo.scopeID = uuid.Nil }, ports.ErrRejected},
		{"file", func(f *untrackedOrganizationTestFixture) { f.repo.fileID = uuid.Nil }, ports.ErrRejected},
		{"confirmation", func(f *untrackedOrganizationTestFixture) { f.repo.input.ConfirmationID = uuid.Nil }, ports.ErrRejected},
		{"empty key", func(f *untrackedOrganizationTestFixture) { f.repo.key = "" }, ports.ErrRejected},
		{"key path", func(f *untrackedOrganizationTestFixture) { f.repo.key = "../private" }, ports.ErrRejected},
		{"oversize key", func(f *untrackedOrganizationTestFixture) { f.repo.key = strings.Repeat("a", 161) }, ports.ErrRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			tc.change(f)
			out, err := f.organize(t.Context())
			if out != nil || !errors.Is(err, tc.want) || f.repo.prepares != 0 || f.repo.commits != 0 || f.base.files.opens.Load() != 0 {
				t.Fatal("invalid command crossed the boundary", err)
			}
		})
	}
	for _, method := range []string{"organize", "receipt", "read"} {
		t.Run("current account/"+method, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			f.repo.err = ports.ErrForbidden
			var err error
			switch method {
			case "organize":
				_, err = f.organize(t.Context())
			case "receipt":
				_, err = f.receipt(t.Context())
			case "read":
				_, err = f.read(t.Context())
			}
			if err != ports.ErrForbidden || f.repo.commits != 0 || f.base.files.opens.Load() != 0 {
				t.Fatal("repository current-account recheck bypassed", err)
			}
		})
	}
}

func TestUntrackedOrganizationMalformedPreparationAndMaterial(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*untrackedOrganizationTestFixture)
	}{
		{"nil preparation", func(f *untrackedOrganizationTestFixture) { f.repo.preparation = nil }},
		{"empty preparation", func(f *untrackedOrganizationTestFixture) {
			f.repo.preparation = &ports.UntrackedOrganizationPreparation{}
		}},
		{"ambiguous preparation", func(f *untrackedOrganizationTestFixture) { f.repo.preparation.Receipt = f.repo.receipt }},
		{"operation identity", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.ID = uuid.Nil }},
		{"actor", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.ActorID = uuid.New() }},
		{"scope", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.ScopeID = uuid.New() }},
		{"file", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.FileID = uuid.New() }},
		{"confirmation", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Operation.ConfirmationID = uuid.New()
		}},
		{"plan", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.PlanID = uuid.Nil }},
		{"step", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.StepID = uuid.Nil }},
		{"job", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.JobID = uuid.Nil }},
		{"scope version", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.ScopeVersion++ }},
		{"file revision", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.FileRevision++ }},
		{"hash", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Operation.MaterialHash = strings.Repeat("b", 64)
		}},
		{"creation", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Operation.CreatedAt = time.Time{} }},
		{"creation order", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Operation.CreatedAt = f.repo.capture.material.Confirmation.Material.CreatedAt.Add(-time.Second)
		}},
		{"confirmation key", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Confirmation.CommandKey = "" }},
		{"confirmation state", func(f *untrackedOrganizationTestFixture) { f.repo.capture.material.Confirmation.State = "completed" }},
		{"confirmation permission", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.ExecutionAllowed = true
		}},
		{"confirmation ID", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.ID = uuid.New()
		}},
		{"confirmation actor", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.ActorID = uuid.New()
		}},
		{"scope owner", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Scope.OwnerID = uuid.New()
		}},
		{"disabled", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Scope.Enabled = false
		}},
		{"missing source path", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Scope.LocalRoot = ""
		}},
		{"source traversal", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.File.RelativePath = "../movie.mkv"
		}},
		{"target traversal", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Input.TargetRelativePath = "../movie.mkv"
		}},
		{"action", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Input.Action = "delete"
		}},
		{"target type", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Scope.TargetMediaType = "tv"
		}},
		{"not ready", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Review.State = "review_required"
		}},
		{"reason", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Review.Evidence.Reason = "ambiguous"
		}},
		{"stale review", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Review.Stale = true
		}},
		{"review permission", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Review.ExecutionAllowed = true
		}},
		{"candidate", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Review.Evidence.Candidate = nil
		}},
		{"candidate namespace", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Review.Evidence.Candidate.Namespace = "tmdb.tv"
		}},
		{"file scope", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.File.ScopeID = uuid.New()
		}},
		{"marker", func(f *untrackedOrganizationTestFixture) {
			f.repo.capture.material.Confirmation.Material.Scope.TargetMarker = "../marker"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			tc.change(f)
			out, err := f.organize(t.Context())
			if out != nil || err != ports.ErrUnavailable || f.repo.prepares != 1 || f.repo.commits != 0 || f.base.files.opens.Load() != 0 {
				t.Fatal("malformed repository material reached physical I/O", err)
			}
		})
	}
}

func TestUntrackedOrganizationReceiptIntegrity(t *testing.T) {
	changes := []struct {
		name   string
		change func(*ports.UntrackedOrganizationReceipt)
	}{
		{"key", func(r *ports.UntrackedOrganizationReceipt) { r.CommandKey = "other" }},
		{"state", func(r *ports.UntrackedOrganizationReceipt) { r.State = "running" }},
		{"permission", func(r *ports.UntrackedOrganizationReceipt) { r.ExecutionAllowed = true }},
		{"actor", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.ActorID = uuid.New() }},
		{"scope", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.ScopeID = uuid.New() }},
		{"file", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.FileID = uuid.New() }},
		{"operation", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.ID = uuid.Nil }},
		{"confirmation", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.ConfirmationID = uuid.Nil }},
		{"plan", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.PlanID = uuid.Nil }},
		{"step", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.StepID = uuid.Nil }},
		{"job", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.JobID = uuid.Nil }},
		{"version", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.ScopeVersion = 0 }},
		{"revision", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.FileRevision = 0 }},
		{"hash", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.MaterialHash = "invalid" }},
		{"created", func(r *ports.UntrackedOrganizationReceipt) { r.Operation.CreatedAt = time.Time{} }},
	}
	for _, mode := range []string{"prepare", "receipt", "commit"} {
		for _, tc := range changes {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				f := untrackedOrganizationFixture(t)
				if mode == "commit" {
					f.repo.onCommit = func(_ context.Context, out *ports.UntrackedOrganizationReceipt) { tc.change(out) }
				} else {
					tc.change(f.repo.receipt)
					f.repo.preparation = &ports.UntrackedOrganizationPreparation{Receipt: f.repo.receipt}
				}
				var out *ports.UntrackedOrganizationReceipt
				var err error
				if mode == "receipt" {
					out, err = f.receipt(t.Context())
				} else {
					out, err = f.organize(t.Context())
				}
				if out != nil || err != ports.ErrUnavailable {
					t.Fatal("malformed queued receipt accepted", err)
				}
				if mode != "commit" && f.base.files.opens.Load() != 0 {
					t.Fatal("receipt validation opened a root")
				}
				untrackedReviewAssertClosed(t, f.base.files)
			})
		}
	}
	for _, field := range []string{"operation", "confirmation", "plan", "step", "job", "version", "revision", "hash", "created"} {
		t.Run("valid but substituted/"+field, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			f.repo.onCommit = func(_ context.Context, r *ports.UntrackedOrganizationReceipt) {
				switch field {
				case "operation":
					r.Operation.ID = uuid.New()
				case "confirmation":
					r.Operation.ConfirmationID = uuid.New()
				case "plan":
					r.Operation.PlanID = uuid.New()
				case "step":
					r.Operation.StepID = uuid.New()
				case "job":
					r.Operation.JobID = uuid.New()
				case "version":
					r.Operation.ScopeVersion++
				case "revision":
					r.Operation.FileRevision++
				case "hash":
					r.Operation.MaterialHash = strings.Repeat("b", 64)
				case "created":
					r.Operation.CreatedAt = r.Operation.CreatedAt.Add(time.Second)
				}
			}
			if out, err := f.organize(t.Context()); out != nil || err != ports.ErrUnavailable || f.repo.commits != 1 {
				t.Fatal("commit substituted frozen operation", err)
			}
			untrackedReviewAssertClosed(t, f.base.files)
		})
	}
}

func TestUntrackedOrganizationReadOnlyViews(t *testing.T) {
	for _, state := range []string{"queued", "running", "completed", "review_required", "failed"} {
		for _, nullable := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/nullable=%t", state, nullable), func(t *testing.T) {
				f := untrackedOrganizationFixture(t)
				f.service.files = nil
				f.repo.view.State = state
				f.repo.view.ReviewRequired = state == "review_required"
				if !nullable {
					id, at := uuid.New(), f.repo.view.Operation.CreatedAt.Add(time.Second).In(time.FixedZone("historical", -7*3600))
					f.repo.view.CompletedFileID, f.repo.view.RecordedAt = &id, &at
				}
				before, err := json.Marshal(f.repo.view)
				if err != nil {
					t.Fatal(err)
				}
				out, err := f.read(t.Context())
				if err != nil || out == nil || out.ExecutionAllowed || out.State != state || out.ReviewRequired != f.repo.view.ReviewRequired {
					t.Fatal("read-only state rejected or changed", err)
				}
				after, err := json.Marshal(out)
				if err != nil || string(before) != string(after) {
					t.Fatal("view bytes changed", err)
				}
				if !nullable {
					*out.CompletedFileID = uuid.New()
					*out.RecordedAt = time.Time{}
					if *f.repo.view.CompletedFileID == *out.CompletedFileID || f.repo.view.RecordedAt.IsZero() {
						t.Fatal("view aliases repository pointers")
					}
				}
				if f.repo.reads != 1 || f.repo.prepares != 0 || f.repo.commits != 0 || f.base.files.opens.Load() != 0 {
					t.Fatal("read performed work")
				}
			})
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*untrackedOrganizationTestFixture)
	}{
		{"nil", func(f *untrackedOrganizationTestFixture) { f.repo.view = nil }},
		{"dead must be mapped by repo", func(f *untrackedOrganizationTestFixture) { f.repo.view.State = "dead" }},
		{"unknown", func(f *untrackedOrganizationTestFixture) { f.repo.view.State = "succeeded" }},
		{"permission", func(f *untrackedOrganizationTestFixture) { f.repo.view.ExecutionAllowed = true }},
		{"operation", func(f *untrackedOrganizationTestFixture) { f.repo.view.Operation.ID = uuid.New() }},
		{"owner", func(f *untrackedOrganizationTestFixture) { f.repo.view.Operation.ActorID = uuid.New() }},
		{"zero completed file", func(f *untrackedOrganizationTestFixture) { id := uuid.Nil; f.repo.view.CompletedFileID = &id }},
		{"zero recorded", func(f *untrackedOrganizationTestFixture) { at := time.Time{}; f.repo.view.RecordedAt = &at }},
		{"old recorded", func(f *untrackedOrganizationTestFixture) {
			at := f.repo.view.Operation.CreatedAt.Add(-time.Second)
			f.repo.view.RecordedAt = &at
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := untrackedOrganizationFixture(t)
			tc.change(f)
			if out, err := f.read(t.Context()); out != nil || err != ports.ErrUnavailable || f.base.files.opens.Load() != 0 {
				t.Fatal("malformed view accepted", err)
			}
		})
	}
}

func TestUntrackedOrganizationSafeErrorsAndUnknownCommit(t *testing.T) {
	for _, want := range []error{ports.ErrUntrackedOrganizationStale, ports.ErrForbidden, ports.ErrUnauthorized, ports.ErrNotFound, ports.ErrRejected, ports.ErrEndpointVersionConflict, ports.ErrUnavailable} {
		for _, stage := range []string{"prepare", "commit", "receipt", "read"} {
			t.Run(stage+"/"+want.Error(), func(t *testing.T) {
				f := untrackedOrganizationFixture(t)
				wrapped := fmt.Errorf("private path and credentials: %w", want)
				if stage == "commit" {
					f.repo.commitErr = wrapped
				} else {
					f.repo.err = wrapped
				}
				var err error
				switch stage {
				case "prepare", "commit":
					_, err = f.organize(t.Context())
				case "receipt":
					_, err = f.receipt(t.Context())
				case "read":
					_, err = f.read(t.Context())
				}
				if err != want {
					t.Fatalf("safe sentinel changed or leaked: %v", err)
				}
				untrackedReviewAssertClosed(t, f.base.files)
			})
		}
	}
	f := untrackedOrganizationFixture(t)
	f.repo.commitErr = errors.New("private COMMIT connection lost after queue persistence")
	if out, err := f.organize(t.Context()); out != nil || err != ports.ErrUnavailable || f.repo.prepares != 1 || f.repo.commits != 1 || f.repo.receipts != 0 {
		t.Fatal("unknown commit retried or exposed private error", err)
	}
	f.service.files = nil
	if out, err := f.receipt(t.Context()); err != nil || out == nil || f.repo.commits != 1 {
		t.Fatal("original-key recovery failed", err)
	}
	untrackedReviewAssertClosed(t, f.base.files)
}
