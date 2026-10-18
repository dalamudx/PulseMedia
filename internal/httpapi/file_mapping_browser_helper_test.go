package httpapi_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func startFileMappingBrowser(t *testing.T, ctx context.Context) (string, func()) {
	t.Helper()
	root, e := os.MkdirTemp(os.Getenv("TMPDIR"), "mapping-browser-")
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	chrome := exec.CommandContext(ctx, "/opt/google/chrome/chrome", "--headless=new", "--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port="+fmt.Sprint(port), "--user-data-dir="+filepath.Join(root, "profile"))
	chrome.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	chrome.Cancel = func() error { return syscall.Kill(-chrome.Process.Pid, syscall.SIGKILL) }
	chrome.Stdout = io.Discard
	chrome.Stderr = io.Discard
	if e = chrome.Start(); e != nil {
		_ = os.RemoveAll(root)
		t.Fatal(e)
	}
	cleanup := func() {
		_ = syscall.Kill(-chrome.Process.Pid, syscall.SIGKILL)
		_ = chrome.Wait()
		if e := os.RemoveAll(root); e != nil {
			t.Error(e)
		}
	}
	client := &http.Client{Timeout: time.Second}
	url := "http://" + addr
	until := time.Now().Add(8 * time.Second)
	for ctx.Err() == nil && time.Now().Before(until) {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, url+"/json/version", nil)
		if e != nil {
			cleanup()
			t.Fatal(e)
		}
		r, e := client.Do(req)
		if e == nil {
			_ = r.Body.Close()
			if r.StatusCode == http.StatusOK {
				return url, cleanup
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	cleanup()
	t.Fatal("owned mapping browser CDP not ready")
	return "", func() {}
}
