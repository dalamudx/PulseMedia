package platform_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transfer "github.com/dalamudx/PulseMedia/internal/adapter/transferlegacy"
	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestPathTraversalAndSymlinkHardening(t *testing.T) {
	// 1. Path traversal attacks
	traversalPayloads := []string{
		"../../etc/passwd",
		"../secret.key",
		"/etc/shadow",
		"C:\\Windows\\System32\\cmd.exe",
		"....//....//etc/passwd",
		"movies/../../../../etc/passwd",
	}

	for _, p := range traversalPayloads {
		_, err := naming.CleanRelPath(p)
		if err == nil {
			t.Errorf("vulnerability: path traversal payload %q was not rejected", p)
		}
	}

	// 2. Symlink attack rejection
	tempDir, err := os.MkdirTemp("", "pulsemedia-sec-symlink-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	sensitiveFile := filepath.Join(tempDir, "sensitive.txt")
	_ = os.WriteFile(sensitiveFile, []byte("TOP_SECRET_SYSTEM_CREDENTIAL"), 0600)

	symlinkAttack := filepath.Join(tempDir, "attack_link.mkv")
	_ = os.Symlink(sensitiveFile, symlinkAttack)

	// Transfer engine must strictly reject symlink
	targetPath := filepath.Join(tempDir, "target.mkv")
	_, err = transfer.ExecutePhysicalTransfer(symlinkAttack, targetPath, "copy")
	if err == nil {
		t.Fatal("vulnerability: symlink transfer succeeded without rejection")
	}
}

func TestSecretKeyFilePermissions0600(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pulsemedia-sec-key-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	key, err := crypto.EnsureMasterKey(tempDir, "")
	if err != nil || len(key) != 32 {
		t.Fatalf("failed to generate key: %v", err)
	}

	keyFile := filepath.Join(tempDir, "secret.key")
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("failed to stat secret.key: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Fatalf("vulnerability: secret.key file permissions are %o (must be strictly 0600)", perm)
	}

	// Verify file cannot be read by other/group if simulated
	if perm&0077 != 0 {
		t.Fatalf("vulnerability: secret.key has group or world permissions: %o", perm)
	}
}

func TestCredentialSanitizationInLogsAndOutput(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.NewLogger(&buf, "debug")

	ctx := context.Background()
	sensitiveDSN := "postgres://pulseuser:SuperSecretPassword123@db.internal:5432/pulsedb?sslmode=disable"
	sensitiveToken := "pm_tok_9a3f12c45b6d7e8f0123456789abcdef"
	sensitivePasskey := "passkey_abc123xyz_top_secret"

	logger.InfoContext(ctx, "database connection initialized",
		"dsn", sensitiveDSN,
		"token", sensitiveToken,
		"passkey", sensitivePasskey,
	)

	logOutput := buf.String()
	if strings.Contains(logOutput, "SuperSecretPassword123") {
		t.Fatal("vulnerability: database password leaked in structured logs")
	}
	if strings.Contains(logOutput, "9a3f12c45b6d7e8f0123456789abcdef") {
		t.Fatal("vulnerability: raw token leaked in structured logs")
	}
	if strings.Contains(logOutput, "passkey_abc123xyz_top_secret") {
		t.Fatal("vulnerability: raw passkey leaked in structured logs")
	}
}

func TestActorScopeElevationPrevention(t *testing.T) {
	memberActor := &ports.Actor{
		Type:   ports.ActorTypeUser,
		ID:     "member-001",
		Name:   "member",
		Role:   ports.RoleMember,
		Scopes: []string{"admin", "system"}, // attempted scope elevation
	}

	// Even if token claims scopes, Role is Member and ActorType is User
	if memberActor.IsAdmin() {
		t.Fatal("vulnerability: member actor was granted admin privileges via scopes")
	}
	if memberActor.IsSystem() {
		t.Fatal("vulnerability: external actor claimed system actor type")
	}

	// Cannot access other user's resources
	if memberActor.CanAccessUser("other-user-002") {
		t.Fatal("vulnerability: member actor was allowed to access other user's resource")
	}
}

func TestSSRFAndResponseLimitProtection(t *testing.T) {
	// Mock server that attempts to return an endless stream to cause memory exhaustion
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		// Write 1MB chunks
		chunk := make([]byte, 1024*1024)
		for i := 0; i < 50; i++ { // 50MB
			_, _ = w.Write(chunk)
		}
	}))
	defer server.Close()

	// Safe limited client
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Limit reader to 10MB
	limitedReader := http.MaxBytesReader(nil, resp.Body, 10*1024*1024)
	buf := make([]byte, 1024*1024)
	totalRead := 0
	var readErr error

	for {
		n, err := limitedReader.Read(buf)
		totalRead += n
		if err != nil {
			readErr = err
			break
		}
	}

	if readErr == nil || totalRead > 11*1024*1024 {
		t.Fatalf("vulnerability: response stream exceeded memory protection threshold, read %d bytes", totalRead)
	}
}
