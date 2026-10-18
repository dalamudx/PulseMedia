package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Verifies PG18's actual volume root across container replacement, independently
// of the application image architecture. No host ports or existing volumes are used.
func TestContainerPG18VolumeLayout(t *testing.T) {
	if os.Getenv("PULSE_CONTAINER_TEST_IMAGE") == "" {
		t.Skip("explicit container acceptance opt-in required")
	}
	image := os.Getenv("PULSE_CONTAINER_BASE_IMAGE")
	if image == "" {
		t.Fatal("explicit mise-managed PostgreSQL image required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	owner := uuid.NewString()
	name, volume := "pulse-pg-layout-"+owner, "pulse-pg-layout-data-"+owner
	const label = "io.pulsemedia.layout"
	run := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("owned PG18 volume operation failed: %v", err)
		}
		return strings.TrimSpace(string(output))
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, resource := range []string{"container", "volume"} {
			target := name
			if resource == "volume" {
				target = volume
			}
			if exec.CommandContext(cleanup, "podman", resource, "exists", target).Run() != nil {
				continue
			}
			format := "{{index .Labels \"" + label + "\"}}"
			if resource == "container" {
				format = "{{index .Config.Labels \"" + label + "\"}}"
			}
			actual, err := exec.CommandContext(cleanup, "podman", resource, "inspect", "--format", format, target).Output()
			if err != nil || strings.TrimSpace(string(actual)) != owner {
				t.Error("layout cleanup ownership mismatch")
				return
			}
			args := []string{resource, "rm"}
			if resource == "container" {
				args = append(args, "--force", "--volumes")
			}
			args = append(args, target)
			if err := exec.CommandContext(cleanup, "podman", args...).Run(); err != nil {
				t.Error("owned layout resource cleanup failed")
				return
			}
			if exec.CommandContext(cleanup, "podman", resource, "exists", target).Run() == nil {
				t.Error("owned layout resource remains")
				return
			}
		}
	})
	run("volume", "create", "--label", label+"="+owner, volume)
	start := func() {
		t.Helper()
		run("run", "--detach", "--platform", "linux/"+runtime.GOARCH, "--name", name,
			"--label", label+"="+owner, "--network", "none", "--env", "POSTGRES_HOST_AUTH_METHOD=trust",
			"--mount", "type=volume,source="+volume+",destination=/var/lib/postgresql", image,
			"postgres", "-c", "listen_addresses=")
		for {
			command := exec.CommandContext(ctx, "podman", "exec", name, "sh", "-c", `test "$(cat /proc/1/comm)" = postgres && exec psql -U postgres -d postgres -Atqc 'SHOW server_version_num'`)
			b, err := command.Output()
			if err == nil && strings.TrimSpace(string(b)) == "180006" {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("owned PG18 layout database did not become ready")
			case <-time.After(150 * time.Millisecond):
			}
		}
	}
	sql := func(query string) string {
		return run("exec", name, "psql", "--no-psqlrc", "--set", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-Atqc", query)
	}
	start()
	if sql("SHOW data_directory") != "/var/lib/postgresql/18/docker" {
		t.Fatal("unexpected PG18 physical data directory")
	}
	var mounts []struct{ Name, Destination string }
	if json.Unmarshal([]byte(run("inspect", "--format", "{{json .Mounts}}", name)), &mounts) != nil {
		t.Fatal("mount inspection failed")
	}
	owned := false
	for _, mount := range mounts {
		if mount.Destination == "/var/lib/postgresql" && mount.Name == volume {
			owned = true
		}
	}
	if !owned {
		t.Fatal("PG18 data is not in the explicitly owned parent volume")
	}
	sql("CREATE TABLE layout_probe (id integer PRIMARY KEY, note text NOT NULL); INSERT INTO layout_probe VALUES (1, 'retained across replacement')")
	run("stop", "--time", "15", name)
	if run("inspect", "--format", "{{.State.ExitCode}}", name) != "0" {
		t.Fatal("PG18 volume writer did not stop cleanly")
	}
	run("rm", "--volumes", name)
	run("volume", "exists", volume)
	start()
	if sql("SELECT note FROM layout_probe WHERE id=1") != "retained across replacement" {
		t.Fatal("container replacement lost the persistent PG18 data")
	}
	t.Log("PG18_VOLUME_LAYOUT_PASSED parent=/var/lib/postgresql pgdata=/var/lib/postgresql/18/docker recreated_container=true data_preserved=true")
}
