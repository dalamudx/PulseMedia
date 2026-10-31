package httpapi

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClassificationAndExecutionResponsesNeverExposeRawErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{ports.ErrUnauthorized, 401}, {ports.ErrForbidden, 403}, {ports.ErrNotFound, 404}, {ports.ErrClassificationConflict, 409}, {ports.ErrClassificationEvidence, 409}, {ports.ErrInvalidClassification, 400}, {errors.New("https://private.invalid/passkey"), 503}} {
		w := httptest.NewRecorder()
		if e := (classificationResponse{err: tc.err}).VisitGetClassificationResponse(w); e != nil {
			t.Fatal(e)
		}
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(w.Body.String(), "private.invalid") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	if e := (executionResponse{err: errors.New("postgres://credential")}).VisitGetSubscriptionExecutionResponse(w); e != nil {
		t.Fatal(e)
	}
	if w.Code != 503 || w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(w.Body.String(), "credential") {
		t.Fatal(w.Code, w.Body.String())
	}
}
