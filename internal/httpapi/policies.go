package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
)

type policyHTTPError struct {
	Status int
	Body   ErrorResponse
}

func policyFailure(ctx context.Context, e error) policyHTTPError {
	status, code := 503, "UNAVAILABLE"
	switch {
	case errors.Is(e, ports.ErrUnauthorized):
		status, code = 401, "UNAUTHORIZED"
	case errors.Is(e, ports.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(e, ports.ErrNotFound):
		status, code = 404, "NOT_FOUND"
	case errors.Is(e, ports.ErrInvalidPolicy):
		status, code = 400, "INVALID_POLICY"
	case errors.Is(e, ports.ErrPolicyConflict):
		status, code = 409, "VERSION_CONFLICT"
	}
	return policyHTTPError{status, ErrorResponse{Code: code, Message: "policy request could not be completed", RequestId: GetRequestID(ctx)}}
}
func (e policyHTTPError) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(e.Status)
	return json.NewEncoder(w).Encode(e.Body)
}
func (e policyHTTPError) VisitListPoliciesResponse(w http.ResponseWriter) error { return e.write(w) }
func (e policyHTTPError) VisitCreatePolicyResponse(w http.ResponseWriter) error { return e.write(w) }
func (e policyHTTPError) VisitGetPolicyRevisionResponse(w http.ResponseWriter) error {
	return e.write(w)
}
func (e policyHTTPError) VisitPublishPolicyRevisionResponse(w http.ResponseWriter) error {
	return e.write(w)
}
func (e policyHTTPError) VisitPreviewPolicyResponse(w http.ResponseWriter) error { return e.write(w) }
func policyRules(in PolicyRuleSet) quality.PolicyRules {
	r := quality.PolicyRules{CutoffScore: in.CutoffScore, PreferHDR: in.PreferHdr, ResolutionOrder: map[quality.Resolution]int{}, SourceOrder: map[quality.Source]int{}, RejectTerms: in.RejectTerms, RejectGroups: in.RejectGroups}
	for k, v := range in.ResolutionOrder {
		r.ResolutionOrder[quality.Resolution(k)] = v
	}
	for k, v := range in.SourceOrder {
		r.SourceOrder[quality.Source(k)] = v
	}
	return r
}
func policyDocument(in ports.PolicyDocument) PolicyDocument {
	r := PolicyRuleSet{CutoffScore: in.Rules.CutoffScore, PreferHdr: in.Rules.PreferHDR, ResolutionOrder: map[string]int{}, SourceOrder: map[string]int{}, RejectTerms: in.Rules.RejectTerms, RejectGroups: in.Rules.RejectGroups}
	if r.RejectTerms == nil {
		r.RejectTerms = []string{}
	}
	if r.RejectGroups == nil {
		r.RejectGroups = []string{}
	}
	for k, v := range in.Rules.ResolutionOrder {
		r.ResolutionOrder[string(k)] = v
	}
	for k, v := range in.Rules.SourceOrder {
		r.SourceOrder[string(k)] = v
	}
	return PolicyDocument{Id: in.ID, Name: in.Name, Revision: in.Revision, Rules: r}
}
func (s *ServerHandler) ListPolicies(ctx context.Context, r ListPoliciesRequestObject) (ListPoliciesResponseObject, error) {
	if GetActor(ctx) == nil {
		return policyFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.polSvc == nil {
		return policyFailure(ctx, ports.ErrUnavailable), nil
	}
	rows, e := s.polSvc.List(ctx)
	if e != nil {
		return policyFailure(ctx, e), nil
	}
	items := []PolicyDocument{}
	for _, row := range rows {
		items = append(items, policyDocument(row))
	}
	return ListPolicies200JSONResponse{Items: items}, nil
}
func (s *ServerHandler) GetPolicyRevision(ctx context.Context, r GetPolicyRevisionRequestObject) (GetPolicyRevisionResponseObject, error) {
	if GetActor(ctx) == nil {
		return policyFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.polSvc == nil {
		return policyFailure(ctx, ports.ErrUnavailable), nil
	}
	p, e := s.polSvc.Read(ctx, r.Id, r.Revision)
	if e != nil {
		return policyFailure(ctx, e), nil
	}
	return GetPolicyRevision200JSONResponse(policyDocument(*p)), nil
}
func (s *ServerHandler) CreatePolicy(ctx context.Context, r CreatePolicyRequestObject) (CreatePolicyResponseObject, error) {
	if GetActor(ctx) == nil {
		return policyFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.polSvc == nil {
		return policyFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return policyFailure(ctx, ports.ErrInvalidPolicy), nil
	}
	rules := policyRules(r.Body.Rules)
	p, e := s.polSvc.Command(ctx, GetActor(ctx), ports.PolicyCommand{Kind: "create", ID: r.Body.Id, Name: r.Body.Name, Rules: &rules}, r.Params.IdempotencyKey)
	if e != nil {
		return policyFailure(ctx, e), nil
	}
	return CreatePolicy201JSONResponse{Id: p.ID, Revision: p.Revision}, nil
}
func (s *ServerHandler) PublishPolicyRevision(ctx context.Context, r PublishPolicyRevisionRequestObject) (PublishPolicyRevisionResponseObject, error) {
	if GetActor(ctx) == nil {
		return policyFailure(ctx, ports.ErrUnauthorized), nil
	}
	if s.polSvc == nil {
		return policyFailure(ctx, ports.ErrUnavailable), nil
	}
	if r.Body == nil {
		return policyFailure(ctx, ports.ErrInvalidPolicy), nil
	}
	c := ports.PolicyCommand{Kind: "publish", ID: r.Id, ExpectedRevision: r.Body.ExpectedRevision, RestoreRevision: r.Body.RestoreRevision}
	if r.Body.Rules != nil {
		rules := policyRules(*r.Body.Rules)
		c.Rules = &rules
	}
	if c.RestoreRevision != nil {
		c.Kind = "restore"
	}
	p, e := s.polSvc.Command(ctx, GetActor(ctx), c, r.Params.IdempotencyKey)
	if e != nil {
		return policyFailure(ctx, e), nil
	}
	return PublishPolicyRevision201JSONResponse{Id: p.ID, Revision: p.Revision}, nil
}
