package postgres_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestCompleteTVWatchTargetCursorIdentityPG(t *testing.T) {
	f, s, source, h := tvWatch(t)
	raw, _ := json.Marshal(struct {
		Kind    string
		Binding ports.DownloadWatchBinding
		Hint    ports.RootChangeHint
	}{"complete_tv_watch_targets_v1", source.Binding, h})
	digest := sha256.Sum256(raw)
	raw, _ = json.Marshal(struct {
		Key   string `json:"key"`
		Sub   string `json:"sub"`
		Scope string `json:"scope"`
	}{uuid.Nil.String(), "", hex.EncodeToString(digest[:])})
	cursor := base64.RawURLEncoding.EncodeToString(raw)
	page, e := s.ResolveDownloadWatchTargets(t.Context(), source.Binding, h, ports.DownloadWatchTargetQuery{Cursor: cursor, Limit: 1})
	if e != nil || len(page.Targets) != 1 || !page.Complete || page.Targets[0].TrackingHintCapture.TrackingHintRequest().HintID != f.r.HintID {
		t.Fatal("original cursor failed", e)
	}
	for _, kind := range []string{"event", "instance", "sequence", "received", "horizon", "source", "path", "kind", "binding"} {
		t.Run(kind, func(t *testing.T) {
			changed, b := h, source.Binding
			switch kind {
			case "event":
				changed.Metadata.EventID = uuid.New()
			case "instance":
				changed.Metadata.InstanceID = uuid.New()
			case "sequence":
				changed.Metadata.Sequence++
			case "received":
				changed.Metadata.ReceivedAt = changed.Metadata.ReceivedAt.Add(time.Nanosecond)
			case "horizon":
				changed.Metadata.ManifestAt = changed.Metadata.ManifestAt.Add(time.Nanosecond)
			case "source":
				changed.Metadata.SourceIdentity.Device++
			case "path":
				changed.RelativePath = "Show/Show.S01E02.mkv"
			case "kind":
				changed.Kind = ports.RootChangeMovedTo
			case "binding":
				b.LocalRoot = "/other"
			}
			if _, e := s.ResolveDownloadWatchTargets(t.Context(), b, changed, ports.DownloadWatchTargetQuery{Cursor: cursor}); !errors.Is(e, ports.ErrRejected) {
				t.Fatal("cursor reused across identity", e)
			}
		})
	}
}

func TestCompleteTVWatchSourceCoveragePG(t *testing.T) {
	for _, kind := range []string{"baseline", "offline", "invalid-mapping", "released", "latest-failed"} {
		t.Run(kind, func(t *testing.T) {
			f, s, source, _ := tvWatch(t)
			issue := ""
			switch kind {
			case "baseline":
				execIntent(t, f.p, `UPDATE library_root SET baseline_completed=false WHERE id=$1`, source.Binding.RootID)
				issue = "baseline_unavailable"
			case "offline":
				execIntent(t, f.p, `UPDATE library_root SET health='offline' WHERE id=$1`, source.Binding.RootID)
				issue = "root_unavailable"
			case "invalid-mapping":
				execIntent(t, f.p, `UPDATE download_client SET remote_path_map='{"/remote":"/"}' WHERE id=$1`, source.Binding.ClientID)
				issue = "invalid_mapping"
			case "released":
				execIntent(t, f.p, `UPDATE acquisition_reservation SET released_at=clock_timestamp() WHERE operation_id=$1`, f.r.OperationID)
			case "latest-failed":
				execIntent(t, f.p, `INSERT INTO job(id,kind,state,payload) SELECT $1,kind,'dead',payload FROM job WHERE id=$2`, uuid.New(), f.r.PriorJobID)
			}
			page, e := s.ListDownloadWatchSources(t.Context(), ports.DownloadWatchSourceQuery{})
			if e != nil || len(page.Sources) != 0 {
				t.Fatal(page, e)
			}
			if issue != "" {
				if page.Complete || len(page.Issues) != 1 || page.Issues[0].Reason != issue {
					t.Fatal(page)
				}
			} else if !page.Complete || len(page.Issues) != 0 {
				t.Fatal(page)
			}
		})
	}
}
