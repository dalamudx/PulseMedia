package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type credentialProbeLogValue struct{}

func (credentialProbeLogValue) LogValue() slog.Value {
	return slog.GroupValue(slog.String("api_key", "fixture-resolved"), slog.String("safe", "retained"))
}

func TestEmbeddedAndMalformedURLQueriesRedacted(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		markers    []string
	}{
		{"embedded", "request https://fixture.invalid/?api_key=fixture-message&state=ok failed", []string{"fixture-message"}},
		{"multiple", "from https://one.invalid/?token=fixture-one to https://two.invalid/?passkey=fixture-two failed", []string{"fixture-one", "fixture-two"}},
		{"malformed-path", "request https://fixture.invalid/%zz?api_key=fixture-malformed&state=ok failed", []string{"fixture-malformed"}},
		{"encoded-key", "request https://fixture.invalid/?%61pi_key=fixture-encoded&state=ok failed", []string{"fixture-encoded"}},
		{"duplicate", "request https://fixture.invalid/?token=fixture-first&token=fixture-last&state=ok failed", []string{"fixture-first", "fixture-last"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactString(tc.text)
			for _, m := range tc.markers {
				if strings.Contains(out, m) {
					t.Fatal("query credential remained in message")
				}
			}
			if !strings.Contains(out, "***") || !strings.Contains(out, "failed") {
				t.Fatal("safe diagnostic context lost")
			}
			if strings.Contains(tc.text, "state=ok") && !strings.Contains(out, "state=ok") {
				t.Fatal("public query field lost")
			}
		})
	}
}

func TestTypedAndBoundAttributesDoNotBypassRedaction(t *testing.T) {
	var out bytes.Buffer
	logger := NewLogger(&out, "debug").With(slog.Any("bound", errors.New("request https://fixture.invalid/?token=fixture-bound failed"))).WithGroup("worker")
	details := map[string]any{"api_key": "fixture-map", "safe": "retained", "nested": map[string]string{"cookie": "fixture-nested", "state": "ok"}}
	before, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	logger.ErrorContext(context.Background(), "request https://fixture.invalid/?api_key=fixture-message failed",
		slog.Any("failure", errors.New("request https://fixture.invalid/?passkey=fixture-error&state=ok failed")),
		slog.Any("details", details), slog.Any("resolved", credentialProbeLogValue{}),
		slog.Any("opaque", struct{ SecretKey string }{SecretKey: "fixture-opaque"}))
	for _, m := range []string{"fixture-bound", "fixture-message", "fixture-error", "fixture-map", "fixture-nested", "fixture-resolved", "fixture-opaque"} {
		if strings.Contains(out.String(), m) {
			t.Fatal("typed attribute or message leaked synthetic credential")
		}
	}
	var row map[string]any
	if err := json.Unmarshal(out.Bytes(), &row); err != nil {
		t.Fatal("redacted record invalid JSON", err)
	}
	worker, ok := row["worker"].(map[string]any)
	if !ok {
		t.Fatal("structured logging group lost")
	}
	safe, ok := worker["details"].(map[string]any)
	if !ok || safe["safe"] != "retained" {
		t.Fatal("safe structured fields lost")
	}
	resolved, ok := worker["resolved"].(map[string]any)
	if !ok || resolved["safe"] != "retained" {
		t.Fatal("resolved safe fields lost")
	}
	if !strings.Contains(worker["failure"].(string), "state=ok") {
		t.Fatal("safe error context lost")
	}
	after, err := json.Marshal(details)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("input details mutated")
	}
}

func TestCyclicLogMapsAreBoundedWithoutMutation(t *testing.T) {
	var out bytes.Buffer
	cycle := map[string]any{"safe": "retained", "token": "fixture-cycle"}
	cycle["loop"] = cycle
	NewLogger(&out, "info").Info("cycle", slog.Any("details", cycle))
	if out.Len() > 10000 || strings.Contains(out.String(), "fixture-cycle") {
		t.Fatal("cycle handling is unbounded or leaked credential")
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "retained") {
		t.Fatal("bounded redacted log lost safe context")
	}
	if cycle["token"] != "fixture-cycle" {
		t.Fatal("input map was mutated")
	}
}
