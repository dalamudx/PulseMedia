package app

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestCompleteTVWatchHintMetadata(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	app := &App{clock: clock.NewMock(now), idgen: idgen.New()}
	source := &downloadWatchSourceRun{instanceID: uuid.New(), manifestAt: now.Add(-time.Second)}
	hint := ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: "Show/Episode.mkv", Metadata: ports.RootChangeMetadata{Sequence: 999, EventID: uuid.New()}}
	physical := ports.FileIdentity{Device: 1, Inode: 321, Size: 4096, Mtime: 123}
	first, err := app.decorateDownloadWatchHint(source, physical, hint)
	if err != nil {
		t.Fatal(err)
	}
	if first.Metadata.EventID == uuid.Nil || first.Metadata.EventID == hint.Metadata.EventID || first.Metadata.InstanceID != source.instanceID || first.Metadata.Sequence != 1 || first.Metadata.ReceivedAt != now || first.Metadata.ManifestAt != source.manifestAt || first.Metadata.SourceIdentity != (ports.FileIdentity{Device: 1, Inode: 321}) {
		t.Fatal("receipt not bound to owned source")
	}
	second, err := app.decorateDownloadWatchHint(source, physical, hint)
	if err != nil {
		t.Fatal(err)
	}
	if second.Metadata.Sequence != 2 || second.Metadata.EventID == first.Metadata.EventID || second.Metadata.InstanceID != first.Metadata.InstanceID {
		t.Fatal("separate events reused identity")
	}
	// A delayed pagination continuation carries the value captured at receipt.
	source.metadataMu.Lock()
	source.manifestAt = now.Add(time.Second)
	source.metadataMu.Unlock()
	if first.Metadata.ManifestAt != now.Add(-time.Second) || first.Metadata.Sequence != 1 || first.Metadata.ReceivedAt != now {
		t.Fatal("receipt changed on manifest refresh")
	}
}

func TestCompleteTVWatchOrdinaryAndRescanNoEarlyMetadata(t *testing.T) {
	now := time.Now()
	for _, rescan := range []bool{false, true} {
		s := &downloadWatchSourceRun{}
		h := ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: "file.mkv", Metadata: ports.RootChangeMetadata{Sequence: 9}}
		if rescan {
			s.manifestAt = now
			h.Kind = ports.RootChangeRescan
			h.RelativePath = "."
		}
		var app *App
		out, err := app.decorateDownloadWatchHint(s, ports.FileIdentity{}, h)
		if err != nil || out.Metadata != (ports.RootChangeMetadata{}) || s.sequence != 0 {
			t.Fatal("ordinary/rescan obtained early authority", err)
		}
	}
}

func TestCompleteTVWatchHintMetadataFailsClosed(t *testing.T) {
	now := time.Now()
	for _, test := range []string{"app", "clock", "ids", "instance", "inode", "overflow"} {
		t.Run(test, func(t *testing.T) {
			app := &App{clock: clock.NewMock(now), idgen: idgen.New()}
			s := &downloadWatchSourceRun{instanceID: uuid.New(), manifestAt: now}
			physical := ports.FileIdentity{Device: 1, Inode: 5}
			switch test {
			case "app":
				app = nil
			case "clock":
				app.clock = nil
			case "ids":
				app.idgen = nil
			case "instance":
				s.instanceID = uuid.Nil
			case "inode":
				physical.Inode = 0
			case "overflow":
				s.sequence = math.MaxInt64
			}
			_, err := app.decorateDownloadWatchHint(s, physical, ports.RootChangeHint{Kind: ports.RootChangeMovedTo, RelativePath: "file.mkv"})
			if !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("unowned hint accepted", err)
			}
		})
	}
}
