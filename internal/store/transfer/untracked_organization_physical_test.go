package transfer

import (
	"bytes"
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
)

type externalPhysicalFixture struct {
	files    *rootfs.Filesystem
	roots    map[string]ports.TransferRoot
	material ports.UntrackedConfirmationMaterial
	proof    ports.TransferContent
	step     uuid.UUID
}

func newExternalPhysicalFixture(t *testing.T, action string) externalPhysicalFixture {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	sourcePath, targetPath := filepath.Join(base, "source"), filepath.Join(base, "target")
	for _, p := range []string{sourcePath, targetPath} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	body := bytes.Repeat([]byte("external-source-content\n"), 4096)
	if err := os.WriteFile(filepath.Join(sourcePath, "video.mkv"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetPath, ".pulsemedia_root"), []byte("root"), 0600); err != nil {
		t.Fatal(err)
	}
	files := rootfs.NewFilesystem()
	roots, release, err := files.LockRoots(ctx, sourcePath, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	sourceID, err := roots[sourcePath].Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := roots[targetPath].Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := roots[sourcePath].Inspect(ctx, "video.mkv")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := files.InspectContent(ctx, roots[sourcePath], "video.mkv", identity)
	if err != nil {
		t.Fatal(err)
	}
	material := ports.UntrackedConfirmationMaterial{
		Scope: ports.UntrackedScope{LocalRoot: sourcePath, SourceIdentity: sourceID, TargetRootPath: targetPath, TargetIdentity: targetID, TargetMarker: ".pulsemedia_root"},
		File:  ports.UntrackedDiscoveredFile{RelativePath: "video.mkv", Identity: identity},
		Input: ports.UntrackedConfirmationInput{Action: action, TargetRelativePath: "video.mkv"},
	}
	return externalPhysicalFixture{files: files, roots: roots, material: material, proof: proof, step: uuid.New()}
}

func (f externalPhysicalFixture) publish(ctx context.Context) error {
	m := f.material
	if m.Input.Action == "hardlink" {
		return f.files.Hardlink(ctx, f.roots[m.Scope.LocalRoot], m.File.RelativePath, f.roots[m.Scope.TargetRootPath], m.Input.TargetRelativePath, f.proof.File)
	}
	_, err := f.files.TransferVerified(ctx, f.roots[m.Scope.LocalRoot], m.File.RelativePath, f.roots[m.Scope.TargetRootPath], m.Input.TargetRelativePath, f.proof, m.Input.Action, f.step)
	return err
}

func TestExternalTransferPostconditions(t *testing.T) {
	for _, action := range []string{"hardlink", "copy", "move", "reflink"} {
		t.Run(action, func(t *testing.T) {
			f := newExternalPhysicalFixture(t, action)
			ctx := context.Background()
			err := f.publish(ctx)
			if action == "reflink" && errors.Is(err, ports.ErrFileUnsupported) {
				if _, e := f.roots[f.material.Scope.LocalRoot].Inspect(ctx, "video.mkv"); e != nil {
					t.Fatal(e)
				}
				if _, e := f.roots[f.material.Scope.TargetRootPath].Inspect(ctx, "video.mkv"); !errors.Is(e, os.ErrNotExist) {
					t.Fatalf("unsupported reflink published target: %v", e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(f.material.Scope.TargetRootPath, "unrelated.txt"), []byte("directory changes are allowed"), 0600); err != nil {
				t.Fatal(err)
			}
			first, err := observeExternalTransfer(ctx, f.files, f.roots, f.material, f.proof, f.step)
			if err != nil {
				t.Fatal(err)
			}
			second, err := observeExternalTransfer(ctx, f.files, f.roots, f.material, f.proof, f.step)
			if err != nil || first != second {
				t.Fatalf("observation not stable: %v", err)
			}
			if first.Target.SHA256 != f.proof.SHA256 || first.Target.File.Size != f.proof.File.Size || first.SourceRemoved != (action == "move") {
				t.Fatalf("wrong postcondition: %+v", first)
			}
			same := externalTransferIdentity(first.Target.File, f.proof.File)
			if same != (action == "hardlink") {
				t.Fatalf("wrong destination inode for %s", action)
			}
		})
	}
}

func TestExternalTransferModifiedDestinationRejected(t *testing.T) {
	f := newExternalPhysicalFixture(t, "copy")
	ctx := context.Background()
	if err := f.publish(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.material.Scope.TargetRootPath, "video.mkv"), bytes.Repeat([]byte("x"), int(f.proof.File.Size)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeExternalTransfer(ctx, f.files, f.roots, f.material, f.proof, f.step); err == nil {
		t.Fatal("modified destination accepted")
	}
	source, err := f.files.InspectContent(ctx, f.roots[f.material.Scope.LocalRoot], "video.mkv", f.proof.File)
	if err != nil || source != f.proof {
		t.Fatalf("source changed: %v", err)
	}
}

func TestExternalTransferReplacedRootRejected(t *testing.T) {
	f := newExternalPhysicalFixture(t, "hardlink")
	ctx := context.Background()
	if err := f.publish(ctx); err != nil {
		t.Fatal(err)
	}
	target := f.material.Scope.TargetRootPath
	if err := os.Rename(target, target+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".pulsemedia_root"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeExternalTransfer(ctx, f.files, f.roots, f.material, f.proof, f.step); !errors.Is(err, ports.ErrUntrackedOrganizationStale) {
		t.Fatalf("replacement accepted: %v", err)
	}
}
