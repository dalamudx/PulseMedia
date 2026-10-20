package logging_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/logging"
)

func TestSinkObservesOnlyRedactedRecords(t *testing.T) {
	var sinkLevel slog.Level
	var sinkLine string
	var written bytes.Buffer
	logger := logging.NewLoggerWithSink(&written, "info", func(level slog.Level, line string) {
		sinkLevel, sinkLine = level, line
	})
	logger.Info("fetching", "url", "https://user:secret@example.invalid/?api_key=abcdef", "count", 2)

	if sinkLevel != slog.LevelInfo {
		t.Fatalf("level %v", sinkLevel)
	}
	for _, forbidden := range []string{"secret", "abcdef"} {
		if strings.Contains(sinkLine, forbidden) {
			t.Fatalf("sink leaked %q: %s", forbidden, sinkLine)
		}
	}
	if !strings.HasPrefix(sinkLine, "fetching ") || !strings.Contains(sinkLine, "count=2") {
		t.Fatalf("sink line lost message or safe attribute: %s", sinkLine)
	}
	if !strings.Contains(written.String(), "***") {
		t.Fatal("primary writer did not receive the redacted record")
	}
}

func TestSinkRespectsConfiguredLevel(t *testing.T) {
	var lines []string
	logger := logging.NewLoggerWithSink(&bytes.Buffer{}, "warn", func(_ slog.Level, line string) {
		lines = append(lines, line)
	})
	logger.Info("hidden")
	logger.Warn("shown")
	if len(lines) != 1 || lines[0] != "shown" {
		t.Fatalf("level filter not honored: %v", lines)
	}
}
