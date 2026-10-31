package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This opt-in test runs the actual image and a private schema83 database.
func TestContainerRuntimeSmoke(t *testing.T) {
	image := os.Getenv("PULSE_CONTAINER_TEST_IMAGE")
	if image == "" {
		t.Skip("explicit locally built container image required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := func(args ...string) string {
		t.Helper()
		b, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("owned container operation failed: %v", err)
		}
		return strings.TrimSpace(string(b))
	}
	if command("image", "inspect", "--format", "{{.Config.User}}", image) != "10001:10001" {
		t.Fatal("runtime must default to UID/GID 10001")
	}
	architecture := command("image", "inspect", "--format", "{{.Architecture}}", image)
	if architecture != "amd64" && architecture != "arm64" {
		t.Fatal("unexpected image architecture")
	}
	_, dsn := bootstrapTestPool(t)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || !filepath.IsAbs(cfg.Host) || cfg.Password != "" {
		t.Fatal("private password-free Unix-socket test database required")
	}
	// The socket lives below the wrapper's 0700 task root; only this owned bind
	// mount grants the app UID access. Restore its original modes on exit.
	for _, p := range []string{cfg.Host, filepath.Join(cfg.Host, ".s.PGSQL.5432")} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		path, mode := p, st.Mode().Perm()
		if err := os.Chmod(path, 0777); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(path, mode); err != nil {
				t.Error(err)
			}
		})
	}
	work, err := os.MkdirTemp("/data/temp", "pulse-image-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(work); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(work); !os.IsNotExist(err) {
			t.Error("owned smoke path remains")
		}
	})
	for _, d := range []string{"data", "temp"} {
		p := filepath.Join(work, d)
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(p, 10001, 10001); err != nil {
			t.Fatal("smoke requires ownership-capable host")
		}
	}
	name := "pulse-image-smoke-" + filepath.Base(work)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if exec.CommandContext(cleanup, "podman", "container", "exists", name).Run() != nil {
			return
		}
		owner, err := exec.CommandContext(cleanup, "podman", "inspect", "--format", "{{index .Config.Labels \"io.pulsemedia.smoke\"}}", name).Output()
		if err != nil || strings.TrimSpace(string(owner)) != work {
			t.Error("refusing container cleanup after ownership mismatch")
			return
		}
		if err := exec.CommandContext(cleanup, "podman", "rm", "--force", "--volumes", name).Run(); err != nil {
			t.Error("owned smoke container cleanup failed")
		}
	})
	runtime := command("run", "--rm", "--network", "none", "--read-only", "--entrypoint", "/bin/sh", image, "-ec", "test \"$(id -u)\" = 10001; test -s /etc/ssl/certs/ca-certificates.crt; openssl verify -CAfile /etc/ssl/certs/ca-certificates.crt /usr/share/ca-certificates/mozilla/Amazon_Root_CA_1.crt; pg_dump --version; pg_restore --version; test -r /app/migrations/00083_restore_isolation.sql; test -x /app/pulsebackup; /app/pulsebackup restore --help >/dev/null; test \"$(stat -c %a /data/temp)\" = 700")
	if !strings.Contains(runtime, "pg_dump (PostgreSQL) 18.6 (") || !strings.Contains(runtime, "pg_restore (PostgreSQL) 18.6 (") {
		t.Fatal("PG18.6 clients absent")
	}
	target := fmt.Sprintf("postgres://%s@localhost/%s?host=/pg&sslmode=disable", cfg.User, cfg.Database)
	command("run", "--detach", "--name", name, "--label", "io.pulsemedia.smoke="+work,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--publish", "127.0.0.1::8080", "--env", "PULSE_DATABASE_URL="+target,
		"--env", "PULSE_HTTP_ADDR=0.0.0.0:8080", "--env", "PULSE_LOG_LEVEL=error",
		"--env", "PULSE_SETUP_TOKEN=synthetic-container-setup-token",
		"--mount", "type=bind,source="+cfg.Host+",destination=/pg",
		"--mount", "type=bind,source="+filepath.Join(work, "data")+",destination=/app/data",
		"--mount", "type=bind,source="+filepath.Join(work, "temp")+",destination=/data/temp", image)
	port := command("port", name, "8080/tcp")
	if !strings.HasPrefix(port, "127.0.0.1:") {
		t.Fatal("smoke HTTP must bind loopback")
	}
	base := "http://" + port
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	ready := false
	for !ready {
		resp, err := client.Get(base + "/readyz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			ready = resp.StatusCode == http.StatusOK
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("container readiness failed")
		case <-time.After(150 * time.Millisecond):
		}
	}
	read := func(path string) []byte {
		t.Helper()
		r, err := client.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Body.Close() }()
		b, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil || r.StatusCode != 200 {
			t.Fatal("runtime resource not served")
		}
		return b
	}
	var status struct {
		Initialized bool `json:"initialized"`
	}
	if json.Unmarshal(read("/api/v1/auth/status"), &status) != nil || status.Initialized {
		t.Fatal("fresh DB setup unavailable")
	}
	html := string(read("/setup"))
	asset := regexp.MustCompile(`src="(/assets/[^" ]+\.js)"`).FindStringSubmatch(html)
	if len(asset) != 2 || len(read(asset[1])) < 1000 {
		t.Fatal("embedded production frontend absent")
	}
	post := func(path, body string, setup bool) int {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", base)
		if setup {
			r.Header.Set("X-Setup-Token", "synthetic-container-setup-token")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal("initialization request failed")
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode
	}
	credentials := `{"username":"container-admin","password":"Synthetic-Container-Only-2026!"}`
	if post("/api/v1/auth/setup", credentials, true) != http.StatusOK {
		t.Fatal("real container setup failed")
	}
	if json.Unmarshal(read("/api/v1/auth/status"), &status) != nil || !status.Initialized {
		t.Fatal("setup state not persisted")
	}
	if post("/api/v1/auth/setup", credentials, true) != http.StatusConflict {
		t.Fatal("deployment setup allowed a second administrator")
	}
	if post("/api/v1/auth/logout", `{}`, false) != http.StatusNoContent {
		t.Fatal("initial session logout failed")
	}
	if post("/api/v1/auth/login", credentials, false) != http.StatusOK {
		t.Fatal("real container login failed")
	}
	var current struct {
		Username string `json:"username"`
	}
	if json.Unmarshal(read("/api/v1/auth/me"), &current) != nil || current.Username != "container-admin" {
		t.Fatal("login did not establish the expected account")
	}
	st, err := os.Stat(filepath.Join(work, "data", "secret.key"))
	if err != nil || st.Mode().Perm() != 0600 || st.Size() != 32 {
		t.Fatal("non-root runtime did not publish private master key")
	}
	command("stop", "--time", "15", name)
	if command("inspect", "--format", "{{.State.ExitCode}}", name) != "0" {
		t.Fatal("app did not shut down cleanly")
	}
	t.Logf("CONTAINER_SMOKE_PASSED arch=%s uid=10001 pg=18.6 schema=83 embedded_frontend=true setup_and_login=true setup_once=true read_only_root=true capabilities=none graceful_shutdown=true", architecture)
}
