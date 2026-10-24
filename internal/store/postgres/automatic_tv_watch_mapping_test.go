package postgres

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestAutomaticTVWatchPreparedPathMapping(t *testing.T) {
	root := watchRootMaterial{ClientID: uuid.New(), ClientVersion: time.Now().UTC(), ClientStamp: strings.Repeat("a", 64), ClientType: "qbittorrent", RootID: uuid.New(), RootPath: "/library", Generation: 1, MediaType: "tv", Marker: ".pulsemedia-root", Online: true, BaselineCompleted: true, BaselineID: uuid.New(), Device: 1, Inode: 123, BaselinePath: "/library", BaselineMediaType: "tv", BaselineMarker: ".pulsemedia-root"}
	mapping := map[string]string{"/remote": "/library", "/remote/Show": "/library/Show", "/remote/Show/escape": "/elsewhere"}
	// More than a page of nested sources must remain individually pageable.
	for i := 0; i < 61; i++ {
		mapping[fmt.Sprintf("/remote/sub%03d", i)] = fmt.Sprintf("/library/sub%03d", i)
	}
	root.Mapping, _ = json.Marshal(mapping)
	sources, issues := watchSources(root)
	if len(issues) != 0 || len(sources) != 65 {
		t.Fatalf("sources=%d issues=%v", len(sources), issues)
	}
	var broad, nested ports.DownloadWatchBinding
	for _, b := range sources {
		if b.RemotePrefix == "/remote" {
			broad = b
		}
		if b.RemotePrefix == "/remote/Show" {
			nested = b
		}
	}
	task := watchTaskMaterial{TaskID: uuid.New(), OperationID: uuid.New(), ClientID: root.ClientID, SavePath: "/remote/Show", Root: root}
	if !automaticTVWatchPreparedPath(automaticTVWatchCandidate{watchTaskMaterial: task, PreparedPaths: []string{"Episode.mkv", "Episode.zh.srt"}}, nested, ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: "Episode.mkv"}) {
		t.Fatal("exact prepared video")
	}
	if !automaticTVWatchPreparedPath(automaticTVWatchCandidate{watchTaskMaterial: task, PreparedPaths: []string{"Episode.mkv", "Episode.zh.srt"}}, nested, ports.RootChangeHint{Kind: ports.RootChangeMovedTo, RelativePath: "Episode.zh.srt"}) {
		t.Fatal("exact prepared attachment")
	}
	for _, test := range []struct {
		binding ports.DownloadWatchBinding
		rel     string
		paths   []string
	}{
		{broad, "Show/Episode.mkv", []string{"Episode.mkv"}},
		{nested, "other.mkv", []string{"Episode.mkv"}},
		{nested, "escape/Episode.mkv", []string{"escape/Episode.mkv"}},
		{nested, "Episode.mkv", nil},
	} {
		if automaticTVWatchPreparedPath(automaticTVWatchCandidate{watchTaskMaterial: task, PreparedPaths: test.paths}, test.binding, ports.RootChangeHint{Kind: ports.RootChangeCloseWrite, RelativePath: test.rel}) {
			t.Fatal("sibling/short-prefix/escape accepted", test.rel)
		}
	}
}
