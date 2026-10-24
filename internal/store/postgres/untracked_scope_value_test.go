package postgres

import (
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestUntrackedScopeReceiptValidation(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.FixedZone("original", 8*3600))
	in := ports.UntrackedScopeInput{Enabled: true, ClientID: uuid.New(), ExpectedClientVersion: now, RemotePrefix: "/remote", TargetRootID: uuid.New(), ExpectedRootGeneration: 1}
	c := ports.UntrackedScope{ID: uuid.New(), OwnerID: uuid.New(), Enabled: true, Version: 1, ClientID: in.ClientID, ClientVersion: now, ClientStamp: watchDigest("sealed"), RemotePrefix: in.RemotePrefix, LocalRoot: "/source", SourceIdentity: ports.FileIdentity{Device: 0, Inode: 222}, TargetRootID: in.TargetRootID, TargetRootPath: "/target", TargetRootGeneration: 1, TargetMediaType: "movie", TargetMarker: ".marker", TargetBaselineScanID: uuid.New(), TargetIdentity: ports.FileIdentity{Device: 1, Inode: 111}, CreatedAt: now, UpdatedAt: now}
	input, _ := json.Marshal(in)
	snapshot, _ := json.Marshal(c)
	row := untrackedReceiptRow{ScopeID: c.ID, OwnerID: c.OwnerID, CommandKey: "original", Version: 1, Enabled: true, LocalRoot: c.LocalRoot, RequestHash: untrackedHash(in), Input: input, Snapshot: snapshot}
	r, err := untrackedVerified(row)
	if err != nil || r.ExecutionAllowed || !untrackedSame(c, r.Scope) {
		t.Fatal(r, err)
	}
	in.ExpectedClientVersion = in.ExpectedClientVersion.UTC()
	if untrackedHash(in) != row.RequestHash {
		t.Fatal("instant changed request identity")
	}
	utc := c
	utc.CreatedAt = utc.CreatedAt.UTC()
	utc.UpdatedAt = utc.UpdatedAt.UTC()
	utc.ClientVersion = utc.ClientVersion.UTC()
	if !untrackedSame(c, utc) {
		t.Fatal("same timestamps rejected")
	}
	for _, kind := range []string{"hash", "input", "snapshot", "owner", "version", "path", "enabled"} {
		changed := row
		switch kind {
		case "hash":
			changed.RequestHash = ""
		case "input":
			changed.Input = []byte(`{}`)
		case "snapshot":
			changed.Snapshot = []byte(`{}`)
		case "owner":
			changed.OwnerID = uuid.New()
		case "version":
			changed.Version++
		case "path":
			changed.LocalRoot = "/different"
		case "enabled":
			changed.Enabled = false
		}
		if _, err = untrackedVerified(changed); err == nil {
			t.Fatal("invalid receipt accepted", kind)
		}
	}
}

func TestUntrackedScopeCaptureMaterialCopy(t *testing.T) {
	identity := ports.FileIdentity{Device: 1, Inode: 111}
	c := &untrackedScopeCapture{material: ports.UntrackedScopeMaterial{TargetRoot: ports.ScanRootSnapshot{RootIdentity: &identity}, TargetIdentity: identity}}
	m := c.UntrackedScopeMaterial()
	m.TargetRoot.RootIdentity.Inode = 999
	if c.material.TargetRoot.RootIdentity.Inode != 111 {
		t.Fatal("public material mutated private capture")
	}
}
