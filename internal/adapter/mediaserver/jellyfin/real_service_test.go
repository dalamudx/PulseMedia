package jellyfin_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/jellyfin"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

const m4JellyfinURL = "http://127.0.0.1:18096"
const m4LibraryName = "PulseMedia M4 Color"
const m4RemoteDir = "/media/movies/" + m4LibraryName
const m4RemoteClip = m4RemoteDir + "/PulseMedia M4 Color (2026).mp4"

// TestJellyfinRealService is deliberately opt-in. The owned container, administrator,
// offline library and generated color clip are retained for subsequent M4 work.
// Credentials are read only at runtime; neither commands nor failures print them.
func TestJellyfinRealService(t *testing.T) {
	state := os.Getenv("PULSE_M4_SERVICES_DIR")
	if state == "" {
		t.Skip("set PULSE_M4_SERVICES_DIR to the owned M4 service state")
	}
	if !filepath.IsAbs(state) || filepath.Clean(state) != state || !strings.HasPrefix(state, "/data/temp/pulse-m4-services.") {
		t.Fatal("expected an explicit owned M4 state directory under /data/temp")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	m4VerifyContainer(t, ctx, state) // Must precede any filesystem or HTTP mutation.
	resolved, err := filepath.EvalSymlinks(state)
	if err != nil || resolved != state {
		t.Fatal("state directory must exist without symlink aliases")
	}
	lock, err := os.Open(state)
	if err != nil {
		t.Fatal("open owned state lock")
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("another Jellyfin real-service test owns this state")
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal("open owned state")
	}
	defer func() { _ = root.Close() }()
	for _, dir := range []string{"secrets", "media", "media/movies"} {
		stat, err := root.Lstat(dir)
		if err != nil || !stat.IsDir() {
			t.Fatal("owned credentials/media directories must exist without symlinks")
		}
	}
	scratch, err := os.MkdirTemp("/data/temp", "pulse-jellyfin-test.")
	if err != nil {
		t.Fatal("create scratch directory")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(scratch); err != nil {
			t.Error("remove owned Jellyfin scratch directory")
		}
		if _, err := os.Stat(scratch); !errors.Is(err, os.ErrNotExist) {
			t.Error("Jellyfin scratch cleanup incomplete")
		}
	})
	h := &m4JellyfinHTTP{t: t, ctx: ctx, client: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	var info struct {
		Version, Id            string
		StartupWizardCompleted bool
	}
	h.call("GET", "/System/Info/Public", nil, &info, 200)
	if info.Id == "" || info.Version == "" {
		t.Fatal("missing live server identity/version")
	}
	credentials := m4Credentials(t, root, info.Id, info.StartupWizardCompleted)
	if !info.StartupWizardCompleted {
		h.call("POST", "/Startup/Configuration", map[string]any{"ServerName": "PulseMedia M4", "UICulture": "en-US", "MetadataCountryCode": "US", "PreferredMetadataLanguage": "en"}, nil, 204)
		h.call("GET", "/Startup/FirstUser", nil, nil, 200)
		h.call("POST", "/Startup/User", map[string]string{"Name": credentials.Username, "Password": credentials.Password}, nil, 204)
		h.call("POST", "/Startup/RemoteAccess", map[string]bool{"EnableRemoteAccess": false}, nil, 204)
		h.call("POST", "/Startup/Complete", nil, nil, 204)
		h.call("GET", "/System/Info/Public", nil, &info, 200)
		if !info.StartupWizardCompleted {
			t.Fatal("startup wizard did not complete")
		}
	}
	var login struct{ AccessToken string }
	h.call("POST", "/Users/AuthenticateByName", map[string]string{"Username": credentials.Username, "Pw": credentials.Password}, &login, 200)
	if login.AccessToken == "" {
		t.Fatal("login returned no token")
	}
	h.token = login.AccessToken
	// Reuse only this test's application key. Never print the key inventory.
	var keys struct {
		Items []struct{ AppName, AccessToken string }
	}
	h.call("GET", "/Auth/Keys", nil, &keys, 200)
	keyName := "PulseMedia M4 real adapter"
	key := ""
	for _, entry := range keys.Items {
		if entry.AppName == keyName {
			if key != "" {
				t.Fatal("multiple owned API keys require review")
			}
			key = entry.AccessToken
		}
	}
	if key == "" {
		h.call("POST", "/Auth/Keys?app="+url.QueryEscape(keyName), nil, nil, 204)
		h.call("GET", "/Auth/Keys", nil, &keys, 200)
		for _, entry := range keys.Items {
			if entry.AppName == keyName {
				key = entry.AccessToken
			}
		}
	}
	if key == "" {
		t.Fatal("owned API key unavailable")
	}
	h.call("POST", "/Sessions/Logout", nil, nil, 204)
	h.token = key
	client := jellyfin.NewClient(m4JellyfinURL, key, nil)
	if err := client.Test(ctx); err != nil {
		t.Fatalf("production Test: %v", err)
	}
	wrong := jellyfin.NewClient(m4JellyfinURL, "invalid-"+hex.EncodeToString([]byte(credentials.Username)), nil)
	if err := wrong.Test(ctx); !errors.Is(err, ports.ErrAuthRequired) {
		t.Fatalf("wrong-key Test: %v", err)
	}
	if err := wrong.RefreshPaths(ctx, []string{m4RemoteClip}); !errors.Is(err, ports.ErrAuthRequired) {
		t.Fatalf("wrong-key RefreshPaths: %v", err)
	}
	if _, _, err := wrong.FindByPath(ctx, m4RemoteClip); !errors.Is(err, ports.ErrAuthRequired) {
		t.Fatalf("wrong-key FindByPath: %v", err)
	}

	localDir := "media/movies/" + m4LibraryName
	if err := root.MkdirAll(localDir, 0755); err != nil {
		t.Fatal("create owned media directory")
	}
	if stat, err := root.Lstat(localDir); err != nil || !stat.IsDir() {
		t.Fatal("owned clip directory must not be a symlink")
	}
	type typeOptions struct {
		Type                            string
		MetadataFetchers, ImageFetchers []string
	}
	type libraryOptions struct {
		EnableRealtimeMonitor   bool
		EnableInternetProviders bool
		TypeOptions             []typeOptions
	}
	type folder struct {
		Name, ItemId   string
		Locations      []string
		LibraryOptions libraryOptions
	}
	var folders []folder
	h.call("GET", "/Library/VirtualFolders", nil, &folders, 200)
	owned := false
	for _, f := range folders {
		if f.Name == m4LibraryName {
			if owned || len(f.Locations) != 1 || f.Locations[0] != m4RemoteDir {
				t.Fatal("owned library identity conflict")
			}
			owned = true
		}
	}
	if !owned {
		options := map[string]any{
			"Enabled": true, "EnableRealtimeMonitor": false, "EnableInternetProviders": false,
			"SaveLocalMetadata": false, "AutomaticRefreshIntervalDays": 0,
			"TypeOptions": []typeOptions{{Type: "Movie", MetadataFetchers: []string{}, ImageFetchers: []string{}}},
		}
		h.call("POST", "/Library/VirtualFolders?name="+url.QueryEscape(m4LibraryName)+"&collectionType=movies&paths="+url.QueryEscape(m4RemoteDir)+"&refreshLibrary=true", map[string]any{"LibraryOptions": options}, nil, 204)
	}
	h.call("GET", "/Library/VirtualFolders", nil, &folders, 200)
	verified := false
	libraryID := ""
	for _, f := range folders {
		if f.Name != m4LibraryName {
			continue
		}
		libraryID = f.ItemId
		if f.LibraryOptions.EnableRealtimeMonitor || f.LibraryOptions.EnableInternetProviders {
			t.Fatal("owned library must have monitoring/internet providers disabled")
		}
		for _, opt := range f.LibraryOptions.TypeOptions {
			if opt.Type == "Movie" && opt.MetadataFetchers != nil && len(opt.MetadataFetchers) == 0 && opt.ImageFetchers != nil && len(opt.ImageFetchers) == 0 {
				verified = true
			}
		}
	}
	if !verified {
		t.Fatal("offline Movie metadata configuration was not persisted")
	}
	clip := localDir + "/PulseMedia M4 Color (2026).mp4"
	if stat, err := root.Lstat(clip); err == nil {
		if !stat.Mode().IsRegular() || stat.Size() == 0 {
			t.Fatal("existing owned clip is not a regular nonempty file")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		output := filepath.Join(scratch, "color.mp4")
		cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=320x180:r=24", "-t", "2", "-an", "-c:v", "mpeg4", "-pix_fmt", "yuv420p", "-n", output)
		cmd.Env = append(os.Environ(), "TMPDIR="+scratch, "TMP="+scratch, "TEMP="+scratch)
		if err := cmd.Run(); err != nil {
			t.Fatal("generate original color clip with ffmpeg")
		}
		if err := os.Chmod(output, 0644); err != nil {
			t.Fatal("set clip permissions")
		}
		if err := os.Link(output, filepath.Join(state, clip)); err != nil {
			t.Fatal("publish clip without overwriting existing media")
		}
	} else {
		t.Fatal("inspect owned clip")
	}

	// Bootstrap the new library after publishing the clip: Jellyfin skips empty
	// roots during initial library creation, so file notifications have no indexed
	// parent until this first scan. Subsequent notifications use the adapter only.
	if libraryID == "" {
		t.Fatal("owned library has no item identity")
	}
	if len(folders) != 1 {
		t.Fatal("initial library validation requires this isolated server to contain only the owned library")
	}
	h.call("POST", "/Library/Refresh", nil, nil, 204)
	bootstrapCtx, bootstrapCancel := context.WithTimeout(ctx, 45*time.Second)
	defer bootstrapCancel()
	m4AwaitItem(t, bootstrapCtx, client, "")
	t.Log("initial library validation indexed the exact generated clip")
	var config struct{ LibraryMonitorDelay int }
	h.call("GET", "/System/Configuration", nil, &config, 200)
	if config.LibraryMonitorDelay < 0 || config.LibraryMonitorDelay > 90 {
		t.Fatal("library monitor delay exceeds bounded test budget")
	}
	// Both notifications below use the production adapter, and must preserve one
	// exact-path identity throughout the server's configured debounce interval.
	if err := client.RefreshPaths(ctx, []string{m4RemoteClip}); err != nil {
		t.Fatalf("first RefreshPaths: %v", err)
	}
	id := m4AwaitItem(t, ctx, client, "")
	m4AssertInventory(t, h, id)
	if err := client.RefreshPaths(ctx, []string{m4RemoteClip}); err != nil {
		t.Fatalf("repeat RefreshPaths: %v", err)
	}
	// Observe beyond the real server's debounce window, not an assumed delay.
	until := time.Now().Add(time.Duration(config.LibraryMonitorDelay+5) * time.Second)
	for time.Now().Before(until) {
		m4AwaitItem(t, ctx, client, id)
		m4AssertInventory(t, h, id)
		select {
		case <-ctx.Done():
			t.Fatal("repeat-refresh observation timed out")
		case <-time.After(time.Second):
		}
	}
	if _, found, err := client.FindByPath(ctx, m4RemoteDir+"/absent.mp4"); err != nil || found {
		t.Fatalf("absent exact path: found=%v error=%v", found, err)
	}
	t.Logf("Jellyfin %s: startup complete; production Test/RefreshPaths/FindByPath, wrong-key rejection, exact-path inventory and repeated-refresh stable identity passed", info.Version)
}

type m4AdminCredentials struct{ ServerID, Username, Password string }

func m4Credentials(t *testing.T, root *os.Root, serverID string, completed bool) m4AdminCredentials {
	t.Helper()
	const name = "secrets/jellyfin-admin.json"
	var c m4AdminCredentials
	stat, err := root.Lstat(name)
	if err == nil {
		if !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 {
			t.Fatal("owned credential file must be regular and 0600")
		}
		data, err := root.ReadFile(name)
		if err != nil || json.Unmarshal(data, &c) != nil || c.ServerID != serverID || c.Username == "" || len(c.Password) < 32 {
			t.Fatal("owned credential file is invalid for this server")
		}
		return c
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspect owned credential file")
	}
	if completed {
		t.Fatal("wizard already completed without owned credentials; refusing reset")
	}
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		t.Fatal("generate admin credential")
	}
	c = m4AdminCredentials{ServerID: serverID, Username: "pulsemedia-m4-admin", Password: hex.EncodeToString(password)}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal("encode admin credential")
	}
	staging := "secrets/.jellyfin-admin-" + rand.Text()
	f, err := root.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal("create owned credential staging file")
	}
	defer func() {
		if err := root.Remove(staging); err != nil {
			t.Error("remove owned credential staging file")
		}
	}()
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		t.Fatal("persist complete admin credential")
	}
	if err := root.Link(staging, name); err != nil {
		t.Fatal("publish credential without replacing existing file")
	}
	return c
}

func m4VerifyContainer(t *testing.T, ctx context.Context, state string) {
	t.Helper()
	// Direct podman only; inspect no environment variables or protected files.
	data, err := exec.CommandContext(ctx, "podman", "inspect", "pulsemedia-m4-jellyfin").Output()
	if err != nil {
		t.Fatal("direct podman inspect failed")
	}
	var containers []struct {
		Config struct {
			Labels map[string]string
			User   string
		}
		State  struct{ Running bool }
		Mounts []struct {
			Source, Destination string
			RW                  bool
		}
		NetworkSettings struct {
			Ports map[string][]struct{ HostIp, HostPort string }
		}
	}
	if json.Unmarshal(data, &containers) != nil || len(containers) != 1 {
		t.Fatal("invalid podman container inspection")
	}
	c := containers[0]
	if c.Config.Labels["io.pulsemedia.state"] != state || !c.State.Running || c.Config.User != "1000:1000" {
		t.Fatal("container ownership/runtime mismatch")
	}
	for dest, source := range map[string]string{"/media": "media", "/config": "jellyfin/config", "/cache": "jellyfin/cache"} {
		found := false
		for _, m := range c.Mounts {
			if m.Destination == dest && m.Source == filepath.Join(state, source) && (dest != "/media" || !m.RW) {
				found = true
			}
		}
		if !found {
			t.Fatal("container mount mismatch")
		}
	}
	bindings := c.NetworkSettings.Ports["8096/tcp"]
	if len(bindings) != 1 || bindings[0].HostIp != "127.0.0.1" || bindings[0].HostPort != "18096" {
		t.Fatal("container endpoint mismatch")
	}
}

type m4JellyfinHTTP struct {
	t      *testing.T
	ctx    context.Context
	client *http.Client
	token  string
}

func (h *m4JellyfinHTTP) call(method, path string, body, result any, statuses ...int) {
	h.t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			h.t.Fatal("encode setup request")
		}
	}
	req, err := http.NewRequestWithContext(h.ctx, method, m4JellyfinURL+path, bytes.NewReader(encoded))
	if err != nil {
		h.t.Fatal("construct setup request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", `MediaBrowser Client="PulseMedia M4", Device="Integration test", DeviceId="pulsemedia-m4-jellyfin", Version="1.0"`)
	if h.token != "" {
		req.Header.Set("Authorization", req.Header.Get("Authorization")+`, Token="`+h.token+`"`)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s transport failure", method, strings.Split(path, "?")[0])
	}
	defer func() { _ = resp.Body.Close() }()
	if !slices.Contains(statuses, resp.StatusCode) {
		h.t.Fatalf("%s %s: HTTP %d", method, strings.Split(path, "?")[0], resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		h.t.Fatal("read bounded setup response")
	}
	if result != nil && json.Unmarshal(data, result) != nil {
		h.t.Fatalf("decode %s response", strings.Split(path, "?")[0])
	}
}

func m4AwaitItem(t *testing.T, ctx context.Context, c *jellyfin.Client, expected string) string {
	t.Helper()
	for {
		id, found, err := c.FindByPath(ctx, m4RemoteClip)
		if err != nil {
			t.Fatalf("production FindByPath: %v", err)
		}
		if found {
			if expected != "" && id != expected {
				t.Fatal("refresh changed the exact-path item identity")
			}
			return id
		}
		select {
		case <-ctx.Done():
			t.Fatal("exact clip was not indexed before deadline")
		case <-time.After(time.Second):
		}
	}
}

func m4AssertInventory(t *testing.T, h *m4JellyfinHTTP, expected string) {
	t.Helper()
	var result struct {
		Items            []struct{ Id, Path string }
		TotalRecordCount int
	}
	h.call("GET", "/Items?Recursive=true&Fields=Path&EnableTotalRecordCount=true", nil, &result, 200)
	if result.TotalRecordCount != len(result.Items) {
		t.Fatal("incomplete independent inventory")
	}
	matches := 0
	for _, item := range result.Items {
		if item.Path == m4RemoteClip {
			matches++
			if item.Id != expected {
				t.Fatal("independent exact-path identity mismatch")
			}
		}
	}
	if matches != 1 {
		t.Fatalf("independent exact-path count = %d, want 1", matches)
	}
}
