package ports

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
)

var ErrPolicyConflict = errors.New("policy command conflict")
var ErrInvalidPolicy = errors.New("invalid policy command")

type PolicyDocument struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Revision int64               `json:"revision"`
	Rules    quality.PolicyRules `json:"rules"`
}
type PolicyCommand struct {
	Kind             string               `json:"kind"`
	ID               string               `json:"id"`
	Name             string               `json:"name,omitempty"`
	ExpectedRevision int64                `json:"expected_revision"`
	Rules            *quality.PolicyRules `json:"rules,omitempty"`
	RestoreRevision  *int64               `json:"restore_revision,omitempty"`
}
type PolicyReceipt struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}
type PolicyRepository interface {
	ListPolicies(context.Context) ([]PolicyDocument, error)
	ReadPolicy(context.Context, string, int64) (*PolicyDocument, error) // revision=0 current
	WritePolicy(context.Context, *Actor, PolicyCommand, string, string) (*PolicyReceipt, error)
}
