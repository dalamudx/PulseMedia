package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

const untrackedOrganizationID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
const untrackedOrganizationConfirmationID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
const untrackedOrganizationBody = `{"confirmation_id":"` + untrackedOrganizationConfirmationID + `"}`
const untrackedOrganizationKey = "organization:original_1.key"

var untrackedOrganizationRoutes = []struct{ method, suffix, body, key string }{
	{"POST", "/organizations", untrackedOrganizationBody, untrackedOrganizationKey},
	{"GET", "/organizations/" + untrackedOrganizationID, "", ""},
	{"GET", "/organization-commands/" + untrackedOrganizationKey, "", ""},
}

type untrackedOrganizationHTTPFake struct {
	receipt                      *ports.UntrackedOrganizationReceipt
	view                         *ports.UntrackedOrganizationView
	err                          error
	organizes, receipts, reads   int
	actor                        ports.Actor
	scopeID, fileID, operationID uuid.UUID
	input                        ports.UntrackedOrganizationInput
	key                          string
}

func (f *untrackedOrganizationHTTPFake) track(actor *ports.Actor, scope, file uuid.UUID, key string) {
	f.actor, f.scopeID, f.fileID, f.key = *actor, scope, file, key
}

func (f *untrackedOrganizationHTTPFake) calls() int { return f.organizes + f.receipts + f.reads }

func (f *untrackedOrganizationHTTPFake) Organize(_ context.Context, actor *ports.Actor, scope, file uuid.UUID, input ports.UntrackedOrganizationInput, key string) (*ports.UntrackedOrganizationReceipt, error) {
	f.track(actor, scope, file, key)
	f.organizes++
	f.input = input
	return f.receipt, f.err
}

func (f *untrackedOrganizationHTTPFake) Receipt(_ context.Context, actor *ports.Actor, scope, file uuid.UUID, key string) (*ports.UntrackedOrganizationReceipt, error) {
	f.track(actor, scope, file, key)
	f.receipts++
	return f.receipt, f.err
}

func (f *untrackedOrganizationHTTPFake) Read(_ context.Context, actor *ports.Actor, scope, file, operation uuid.UUID) (*ports.UntrackedOrganizationView, error) {
	f.track(actor, scope, file, "")
	f.reads++
	f.operationID = operation
	return f.view, f.err
}

func untrackedOrganizationHTTPFixture() (*ServerHandler, *untrackedOrganizationHTTPFake) {
	op := ports.UntrackedOrganizationOperation{
		ID: uuid.MustParse(untrackedOrganizationID), ActorID: uuid.MustParse(untrackedScopeAdmin().ID),
		ScopeID: uuid.MustParse(untrackedScopeID), FileID: uuid.MustParse(untrackedReviewFileID),
		ConfirmationID: uuid.MustParse(untrackedOrganizationConfirmationID),
		PlanID:         uuid.MustParse("33333333-3333-4333-8333-333333333333"),
		StepID:         uuid.MustParse("44444444-4444-4444-8444-444444444444"),
		JobID:          uuid.MustParse("55555555-5555-4555-8555-555555555555"),
		ScopeVersion:   2, FileRevision: 3, MaterialHash: strings.Repeat("a", 64),
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 123456000, time.FixedZone("fixture", 8*60*60)),
	}
	f := &untrackedOrganizationHTTPFake{
		receipt: &ports.UntrackedOrganizationReceipt{CommandKey: untrackedOrganizationKey, Operation: op, State: "queued"},
		view:    &ports.UntrackedOrganizationView{Operation: op, State: "queued"},
	}
	s := &ServerHandler{}
	s.SetUntrackedOrganizationService(f)
	return s, f
}

func untrackedOrganizationCall(s *ServerHandler, index int) *httptest.ResponseRecorder {
	route := untrackedOrganizationRoutes[index]
	var keys []string
	if route.key != "" {
		keys = []string{route.key}
	}
	return untrackedScopeCall(untrackedReviewHTTPRouter(s, untrackedScopeAdmin()), route.method, untrackedReviewPath+route.suffix, route.body, keys...)
}

func untrackedOrganizationFields(t *testing.T, raw []byte, want ...string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != len(want) {
		t.Fatalf("unexpected fields: %s (%v)", raw, err)
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Fatal("missing field", key)
		}
	}
	return fields
}

func TestUntrackedOrganizationHTTPOriginalReceiptAndMappings(t *testing.T) {
	s, f := untrackedOrganizationHTTPFixture()
	w := untrackedOrganizationCall(s, 0)
	untrackedReviewHTTPStatus(t, w, 202)
	original := w.Body.String()
	var receipt ports.UntrackedOrganizationReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || receipt.Operation.ID != f.receipt.Operation.ID || untrackedReviewHTTPJSON(t, receipt) != untrackedReviewHTTPJSON(t, *f.receipt) {
		t.Fatal("receipt mapping differs from frozen port", original, err)
	}
	if f.organizes != 1 || f.input.ConfirmationID != uuid.MustParse(untrackedOrganizationConfirmationID) || f.scopeID != f.receipt.Operation.ScopeID || f.fileID != f.receipt.Operation.FileID || f.key != untrackedOrganizationKey || !reflect.DeepEqual(f.actor, *untrackedScopeAdmin()) {
		t.Fatal("actor/scope/file/input/original key mapping", f)
	}
	fields := untrackedOrganizationFields(t, w.Body.Bytes(), "command_key", "operation", "state", "execution_allowed")
	untrackedOrganizationFields(t, fields["operation"], "id", "actor_id", "scope_id", "file_id", "confirmation_id", "plan_id", "step_id", "job_id", "scope_version", "file_revision", "material_hash", "created_at")
	completed, recorded := uuid.MustParse("66666666-6666-4666-8666-666666666666"), f.view.Operation.CreatedAt.Add(time.Minute)
	f.view.State, f.view.CompletedFileID, f.view.RecordedAt = "completed", &completed, &recorded
	w = untrackedOrganizationCall(s, 1)
	untrackedReviewHTTPStatus(t, w, 200)
	var view ports.UntrackedOrganizationView
	if json.Unmarshal(w.Body.Bytes(), &view) != nil || untrackedReviewHTTPJSON(t, view) != untrackedReviewHTTPJSON(t, *f.view) || f.operationID != uuid.MustParse(untrackedOrganizationID) || f.reads != 1 || f.key != "" {
		t.Fatal("view operation/plan/step/job/result mapping", w.Body.String(), f)
	}
	w = untrackedOrganizationCall(s, 2)
	untrackedReviewHTTPStatus(t, w, 200)
	if w.Body.String() != original || f.key != untrackedOrganizationKey || f.receipts != 1 || f.organizes != 1 {
		t.Fatal("receipt GET must preserve original queued bytes and key after completion", w.Body.String(), f)
	}
	w = untrackedOrganizationCall(s, 0)
	untrackedReviewHTTPStatus(t, w, 202)
	if w.Body.String() != original || f.organizes != 2 || f.input.ConfirmationID != receipt.Operation.ConfirmationID {
		t.Fatal("same-key POST must preserve original queued receipt", w.Body.String(), f)
	}
}

func TestUntrackedOrganizationHTTPViewStates(t *testing.T) {
	for _, state := range []string{"queued", "running", "completed", "review_required", "failed"} {
		t.Run(state, func(t *testing.T) {
			s, f := untrackedOrganizationHTTPFixture()
			f.view.State = state
			if state == "completed" || state == "review_required" || state == "failed" {
				recorded := f.view.Operation.CreatedAt.Add(time.Second)
				f.view.RecordedAt = &recorded
			}
			if state == "completed" {
				file := uuid.MustParse("66666666-6666-4666-8666-666666666666")
				f.view.CompletedFileID = &file
			}
			f.view.ReviewRequired = state == "review_required"
			w := untrackedOrganizationCall(s, 1)
			untrackedReviewHTTPStatus(t, w, 200)
			fields := untrackedOrganizationFields(t, w.Body.Bytes(), "operation", "state", "review_required", "completed_file_id", "recorded_at", "execution_allowed")
			if string(fields["execution_allowed"]) != "false" || (state != "completed" && string(fields["completed_file_id"]) != "null") || ((state == "queued" || state == "running") && string(fields["recorded_at"]) != "null") {
				t.Fatal("nullable result/execution contract", w.Body.String())
			}
			if f.calls() != 1 || f.reads != 1 {
				t.Fatal("GET must only read", f)
			}
			if state == "review_required" || state == "failed" {
				f.view.RecordedAt = nil
				w = untrackedOrganizationCall(s, 1)
				untrackedReviewHTTPStatus(t, w, 200)
				if !strings.Contains(w.Body.String(), `"recorded_at":null`) {
					t.Fatal("noncompleted observation time must remain nullable", w.Body.String())
				}
			}
		})
	}
}

func TestUntrackedOrganizationHTTPSafeErrorsAndMissingService(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{"stale", fmt.Errorf("secret: %w", ports.ErrUntrackedOrganizationStale), 409},
		{"conflict", ports.ErrEndpointVersionConflict, 409},
		{"missing", ports.ErrNotFound, 404}, {"foreign-owner", ports.ErrForbidden, 403},
		{"rejected", ports.ErrRejected, 400}, {"unavailable", ports.ErrUnavailable, 503},
		{"unknown", errors.New("secret DSN cookie path"), 503},
		{"canceled", fmt.Errorf("secret: %w", context.Canceled), 503},
		{"deadline", context.DeadlineExceeded, 503},
		{"context-precedes-stale", errors.Join(ports.ErrUntrackedOrganizationStale, context.Canceled), 503},
		{"context-precedes-forbidden", errors.Join(ports.ErrForbidden, context.DeadlineExceeded), 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			for i := range untrackedOrganizationRoutes {
				s, f := untrackedOrganizationHTTPFixture()
				f.err = test.err
				w := untrackedOrganizationCall(s, i)
				untrackedReviewHTTPStatus(t, w, test.status)
				if f.calls() != 1 || strings.Contains(w.Body.String(), "secret") {
					t.Fatal("service error dispatch/disclosure", f, w.Body.String())
				}
			}
		})
	}
	for i := range untrackedOrganizationRoutes {
		untrackedReviewHTTPStatus(t, untrackedOrganizationCall(&ServerHandler{}, i), 503)
	}
}

func TestUntrackedOrganizationHTTPRejectMalformedOperations(t *testing.T) {
	mutations := map[string]func(*ports.UntrackedOrganizationOperation){
		"id":            func(o *ports.UntrackedOrganizationOperation) { o.ID = uuid.Nil },
		"foreign-actor": func(o *ports.UntrackedOrganizationOperation) { o.ActorID = o.PlanID },
		"foreign-scope": func(o *ports.UntrackedOrganizationOperation) { o.ScopeID = o.PlanID },
		"foreign-file":  func(o *ports.UntrackedOrganizationOperation) { o.FileID = o.PlanID },
		"confirmation":  func(o *ports.UntrackedOrganizationOperation) { o.ConfirmationID = uuid.Nil },
		"plan":          func(o *ports.UntrackedOrganizationOperation) { o.PlanID = uuid.Nil },
		"step":          func(o *ports.UntrackedOrganizationOperation) { o.StepID = uuid.Nil },
		"job":           func(o *ports.UntrackedOrganizationOperation) { o.JobID = uuid.Nil },
		"scope-version": func(o *ports.UntrackedOrganizationOperation) { o.ScopeVersion = 0 },
		"file-revision": func(o *ports.UntrackedOrganizationOperation) { o.FileRevision = math.MaxInt64 },
		"hash":          func(o *ports.UntrackedOrganizationOperation) { o.MaterialHash = strings.Repeat("A", 64) },
		"zero-time":     func(o *ports.UntrackedOrganizationOperation) { o.CreatedAt = time.Time{} },
		"invalid-time": func(o *ports.UntrackedOrganizationOperation) {
			o.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			for i := range untrackedOrganizationRoutes {
				s, f := untrackedOrganizationHTTPFixture()
				mutate(&f.receipt.Operation)
				mutate(&f.view.Operation)
				untrackedReviewHTTPStatus(t, untrackedOrganizationCall(s, i), 503)
			}
		})
	}
}

func TestUntrackedOrganizationHTTPRejectMalformedResults(t *testing.T) {
	for _, name := range []string{"nil", "allowed", "receipt-state", "receipt-key", "confirmation-mismatch", "view-id", "state", "review-flag", "completed-missing-file", "completed-zero-file", "completed-missing-time", "noncompleted-file", "recorded-before-created", "recorded-zero"} {
		t.Run(name, func(t *testing.T) {
			for i := range untrackedOrganizationRoutes {
				s, f := untrackedOrganizationHTTPFixture()
				viewOnly := true
				switch name {
				case "nil":
					f.receipt, f.view, viewOnly = nil, nil, false
				case "allowed":
					f.receipt.ExecutionAllowed, f.view.ExecutionAllowed, viewOnly = true, true, false
				case "receipt-state":
					f.receipt.State, viewOnly = "completed", false
				case "receipt-key":
					f.receipt.CommandKey, viewOnly = "other-key", false
				case "confirmation-mismatch":
					if i != 0 {
						continue
					}
					f.receipt.Operation.ConfirmationID, viewOnly = f.receipt.Operation.PlanID, false
				case "view-id":
					f.view.Operation.ID = f.view.Operation.PlanID
				case "state":
					f.view.State = "imported"
				case "review-flag":
					f.view.State = "review_required"
					recorded := f.view.Operation.CreatedAt
					f.view.RecordedAt = &recorded
				case "completed-missing-file", "completed-zero-file", "completed-missing-time":
					f.view.State = "completed"
					recorded, file := f.view.Operation.CreatedAt.Add(time.Second), f.view.Operation.PlanID
					f.view.RecordedAt, f.view.CompletedFileID = &recorded, &file
					if name == "completed-missing-file" {
						f.view.CompletedFileID = nil
					}
					if name == "completed-zero-file" {
						file = uuid.Nil
					}
					if name == "completed-missing-time" {
						f.view.RecordedAt = nil
					}
				case "noncompleted-file":
					f.view.CompletedFileID = &f.view.Operation.PlanID
				case "recorded-before-created", "recorded-zero":
					f.view.State = "failed"
					recorded := f.view.Operation.CreatedAt.Add(-time.Second)
					if name == "recorded-zero" {
						recorded = time.Time{}
					}
					f.view.RecordedAt = &recorded
				}
				if viewOnly && i != 1 {
					continue
				}
				if (name == "receipt-state" || name == "receipt-key") && i == 1 {
					continue
				}
				untrackedReviewHTTPStatus(t, untrackedOrganizationCall(s, i), 503)
			}
		})
	}
}
