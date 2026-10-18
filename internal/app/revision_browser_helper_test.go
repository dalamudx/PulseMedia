package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func startOwnedRevisionChrome(t *testing.T, ctx context.Context) string {
	t.Helper()
	profile := t.TempDir()
	command := exec.CommandContext(ctx, "/opt/google/chrome/chrome", "--headless=new", "--no-sandbox", "--disable-dev-shm-usage", "--no-first-run", "--remote-debugging-port=0", "--user-data-dir="+profile)
	command.Env = append(os.Environ(), "XDG_CONFIG_HOME="+filepath.Join(profile, "config"), "XDG_CACHE_HOME="+filepath.Join(profile, "cache"))
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM); _ = command.Wait() })
	for {
		data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err == nil {
			line, _, _ := strings.Cut(string(data), "\n")
			port, parseErr := strconv.Atoi(line)
			if parseErr == nil && port > 0 && port <= 65535 {
				return "http://127.0.0.1:" + line
			}
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("owned Chrome did not publish its endpoint:", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
