package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/downloader/qbittorrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestCompleteTVTrackingNativePGMetainfo(t *testing.T) {
	for _, kind := range []string{"zero-attachment-complete", "zero-attachment-missing", "extra-file", "empty-inventory", "export-truncated", "export-other-info", "export-outer-metadata"} {
		t.Run(kind, func(t *testing.T) {
			attachment := strings.HasPrefix(kind, "zero-attachment")
			n := newTrackingFixture(t, attachment)
			want := "unknown"
			unavailable := false
			switch kind {
			case "zero-attachment-complete":
				want = "completed"
				n.files = func() string {
					return strings.TrimSuffix(trackingFilesJSON, "]") + `,{"index":2,"name":"Show/note.txt","size":0,"progress":1}]`
				}
			case "zero-attachment-missing": // Same total size, missing zero-byte file is still a mismatch.
			case "extra-file":
				n.files = func() string {
					return strings.TrimSuffix(trackingFilesJSON, "]") + `,{"index":2,"name":"Show/note.txt","size":0,"progress":1}]`
				}
			case "empty-inventory":
				n.files = func() string { return `[]` }
			case "export-truncated":
				unavailable = true
				n.export = func() string { return completeTVRaw[:len(completeTVRaw)-1] }
			case "export-other-info":
				n.export = func() string { return strings.Replace(completeTVRaw, "Show.S01E02", "Show.S01E03", 1) }
			case "export-outer-metadata":
				want = "completed"
				n.export = func() string {
					return strings.Replace(completeTVRaw, "d4:info", "d8:announce14:https://x.test4:info", 1)
				}
			}
			before := n.snapshot(t)
			r, e := n.observe(context.Background())
			if unavailable {
				if r != nil || !errors.Is(e, ports.ErrUnavailable) || n.snapshot(t) != before {
					t.Fatal("invalid export changed protection", r, e)
				}
				return
			}
			if e != nil || r == nil || r.State != want {
				t.Fatal(r, e, "want", want)
			}
			n.assert(t, want)
			if want == "completed" {
				expected := 2
				if attachment {
					expected = 3
				}
				if len(r.Files) != expected {
					t.Fatal("complete inventory receipt", len(r.Files), expected)
				}
				var count int
				e = n.f.a.DBPool().QueryRow(context.Background(), `SELECT jsonb_array_length(result->'files') FROM job WHERE id=$1`, n.request.Lease.ID).Scan(&count)
				if e != nil || count != expected {
					t.Fatal("persistent complete inventory", e, count)
				}
			}
		})
	}
}

type trackingUncancelableTransport struct{}

func (trackingUncancelableTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return http.DefaultTransport.RoundTrip(r.Clone(context.WithoutCancel(r.Context())))
}
func TestCompleteTVTrackingNativePGIgnoredCancellation(t *testing.T) {
	n := newTrackingFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	n.hook = func(p string) {
		if strings.HasSuffix(p, "/files") {
			close(entered)
			<-release
		}
	}
	n.manager.SetClientFactory(func(kind, url, user, password string) (ports.DownloadClient, error) {
		return qbittorrent.NewClient(url, user, password, &http.Client{Transport: trackingUncancelableTransport{}})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := n.observe(ctx); done <- e }()
	joined := false
	defer func() {
		unblock()
		if !joined {
			<-done
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no native inventory")
	}
	cancel()
	select {
	case e := <-done:
		joined = true
		t.Fatal("in-flight read returned before release", e)
	case <-time.After(50 * time.Millisecond):
	}
	if r, e := n.observe(context.Background()); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
		t.Fatal("physical exclusivity released early", r, e)
	}
	unblock()
	e := <-done
	joined = true
	if e != nil {
		t.Fatal("complete physical result erased by cancellation", e)
	}
	n.assert(t, "completed")
}
