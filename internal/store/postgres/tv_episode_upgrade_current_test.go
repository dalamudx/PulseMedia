package postgres_test

import (
	"github.com/dalamudx/PulseMedia/internal/ports"
	"os"
	"path/filepath"
	"testing"
)

func TestTVEpisodeUpgradeRechecksCurrentEvidenceAfterAddPG(t *testing.T) {
	for _, kind := range []string{"actor", "intent", "client", "root"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVUpgradeFixture(t)
			r := f.prepared(t)
			f.native.before = func() {
				switch kind {
				case "actor":
					execIntent(t, f.pool, "UPDATE app_user SET role='member' WHERE id=$1", f.actor.ID)
				case "intent":
					execIntent(t, f.pool, "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1", f.sub)
				case "client":
					execIntent(t, f.pool, "UPDATE download_client SET url='https://changed.invalid' WHERE id=$1", f.client)
				case "root":
					if e := os.Rename(f.rootPath, f.rootPath+".original"); e != nil {
						t.Fatal(e)
					}
					if e := os.Mkdir(f.rootPath, 0755); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(filepath.Join(f.rootPath, ".pulsemedia_root"), []byte("replacement"), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
			_, e := f.service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "send", OperationID: r.OperationID, ExpectedVersion: r.Version, Confirmed: true}, "changed-send")
			if e == nil {
				t.Fatal("stale native return committed", kind)
			}
			if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_episode_upgrade_return WHERE action='send'"); n != 0 {
				t.Fatal("stale return recorded", n)
			}
			if n := intentCount(t, f.pool, "SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL"); n != 1 {
				t.Fatal("stale return lost protection", n)
			}
			_, e = f.service.Command(t.Context(), f.actor, ports.TVEpisodeUpgradeCommand{TVEpisodeUpgradePreviewRequest: f.request, Action: "send", OperationID: r.OperationID, ExpectedVersion: r.Version, Confirmed: true}, "replacement-send")
			if e == nil || f.native.calls.Load() != 1 {
				t.Fatal("stale return replayed", e, f.native.calls.Load())
			}
		})
	}
}