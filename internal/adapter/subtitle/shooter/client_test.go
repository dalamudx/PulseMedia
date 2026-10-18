package shooter_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/subtitle/shooter"
)

func TestShooterSubtitleDownload(t *testing.T) {
	// Mock sub file server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/subapi.php" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[
				{
					"Files": [
						{
							"Ext": "srt",
							"Link": "http://%s/download/sub.srt"
						}
					]
				}
			]`, r.Host)
			return
		}
		if r.URL.Path == "/download/sub.srt" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, "1\n00:00:01,000 --> 00:00:04,000\n你好，世界！\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tempDir, err := os.MkdirTemp("", "pulsemedia-sub-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	// Create dummy video file >= 16KB
	videoFile := filepath.Join(tempDir, "Inception.2010.1080p.mkv")
	payload := make([]byte, 32768)
	_ = os.WriteFile(videoFile, payload, 0644)

	client := shooter.NewClient(server.URL+"/subapi.php", server.Client())
	ctx := context.Background()

	subPath, err := client.DownloadSubtitle(ctx, videoFile)
	if err != nil {
		t.Fatalf("download subtitle failed: %v", err)
	}

	expectedSub := filepath.Join(tempDir, "Inception.2010.1080p.zh-cn.default.srt")
	if subPath != expectedSub {
		t.Fatalf("expected sub path %s, got %s", expectedSub, subPath)
	}

	subBytes, err := os.ReadFile(subPath)
	if err != nil || len(subBytes) == 0 {
		t.Fatalf("subtitle file empty or unreadable: %v", err)
	}
}
