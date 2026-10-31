package tmdb

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/budget"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"testing"
	"time"
)

type failingDiagnosticSettings struct{}

func (failingDiagnosticSettings) GetSetting(context.Context, string) (*ports.SettingItem, error) {
	return nil, errors.New("raw-secret")
}
func (failingDiagnosticSettings) GetSecret(context.Context, string) (string, error) {
	return "", errors.New("raw-secret")
}
func TestMetadataDiagnosticEnvironmentPrecedenceAndGuard(t *testing.T) {
	environment := "environment-key"
	source := NewDiagnosticSource(failingDiagnosticSettings{}, func() string { return environment }, &http.Client{Timeout: time.Second}, budget.NewMetadataBudget(nil))
	session, err := source.OpenDiagnostic(context.Background())
	if err != nil || session == nil {
		t.Fatalf("production environment precedence failed: %v", err)
	}
	environment = "changed-environment-key"
	if !errors.Is(session.Validate(context.Background()), ports.ErrRejected) {
		t.Fatal("environment rotation not fenced")
	}
	environment = ""
	if session.Validate(context.Background()) == nil {
		t.Fatal("unreadable current settings accepted")
	}
	if _, err = source.OpenDiagnostic(context.Background()); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("raw config error escaped: %v", err)
	}
}
func TestMetadataDiagnosticNoBudgetFailsClosed(t *testing.T) {
	source := NewDiagnosticSource(nil, func() string { return "key" }, &http.Client{}, nil)
	if _, err := source.OpenDiagnostic(context.Background()); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatalf("missing shared budget: %v", err)
	}
}
