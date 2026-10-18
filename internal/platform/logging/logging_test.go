package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggingRedaction(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "debug")

	ctx := context.Background()
	logger.InfoContext(ctx, "connecting to db",
		slog.String("db_url", "postgres://user:supersecret@127.0.0.1:5432/pulsemedia?sslmode=disable"),
		slog.String("password", "plaintext_secret"),
		slog.String("api_key", "sk-1234567890"),
		slog.String("auth_token", "eyJhbGciOi..."),
		slog.String("normal_param", "normal_value"),
		slog.String("request_url", "https://api.themoviedb.org/3/movie/550?api_key=topsecret&language=zh-CN"),
	)

	var logged map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logged); err != nil {
		t.Fatalf("failed to parse log JSON: %v, raw: %s", err, buf.String())
	}

	if logged["password"] != "***" {
		t.Fatalf("expected redacted password, got %v", logged["password"])
	}
	if logged["api_key"] != "***" {
		t.Fatalf("expected redacted api_key, got %v", logged["api_key"])
	}
	if logged["auth_token"] != "***" {
		t.Fatalf("expected redacted auth_token, got %v", logged["auth_token"])
	}

	dbURL, ok := logged["db_url"].(string)
	if !ok || strings.Contains(dbURL, "supersecret") {
		t.Fatalf("expected password stripped from db_url, got %v", dbURL)
	}
	if !strings.Contains(dbURL, "***") {
		t.Fatalf("expected masked password in db_url, got %v", dbURL)
	}

	reqURL, ok := logged["request_url"].(string)
	if !ok || strings.Contains(reqURL, "topsecret") {
		t.Fatalf("expected api_key stripped from request_url, got %v", reqURL)
	}

	if logged["normal_param"] != "normal_value" {
		t.Fatalf("expected normal_param intact, got %v", logged["normal_param"])
	}
}

func TestLogLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "warn")

	logger.Debug("debug message")
	logger.Info("info message")
	if buf.Len() > 0 {
		t.Fatalf("expected no log output for debug/info at warn level, got %s", buf.String())
	}

	logger.Warn("warn message")
	if !strings.Contains(buf.String(), "warn message") {
		t.Fatalf("expected warn message in output, got %s", buf.String())
	}
}
