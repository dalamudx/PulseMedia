package command

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

var ErrIdempotencyConflict = ports.ErrCommandConflict

// Fingerprint binds the operation and object as well as the full typed request.
// The explicit contract version prevents interpreting an old payload-only receipt as evidence.
func Fingerprint(action, target string, payload any) (string, error) {
	b, err := json.Marshal(struct {
		Contract string
		Action   string
		Target   string
		Payload  any
	}{"command.v2", action, target, payload})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// LegacyPayloadFingerprint is for verifying existing receipts only; it is never written for new commands.
func LegacyPayloadFingerprint(payload any) (string, error) {
	b, e := json.Marshal(payload)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// AuthorizeSession is an early boundary only. Repositories must recheck live identity in their transaction.
func AuthorizeSession(a *ports.Actor, requiredRole string) error {
	if a == nil {
		return ports.ErrUnauthorized
	}
	if a.Type != ports.ActorTypeUser || a.Scopes != nil {
		return ports.ErrForbidden
	}
	if requiredRole != "" && a.Role != requiredRole {
		return ports.ErrForbidden
	}
	return nil
}
