package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// Only the optional evidence reader is faked; every root probe uses real rootfs.
type completeTVHintReaderTest struct {
	ports.CompleteTVTrackingRepository
	evidence *ports.CompleteTVTrackingHintEvidence
	err      error
	reads    int
	lease    ports.JobLease
}

func (r *completeTVHintReaderTest) ReadCompleteTVTrackingHintForLease(_ context.Context, lease ports.JobLease) (*ports.CompleteTVTrackingHintEvidence, error) {
	r.reads++
	r.lease = lease
	return r.evidence, r.err
}

type completeTVHintNativeTest struct {
	calls  int
	during func()
	err    error
}

func (c *completeTVHintNativeTest) call() error {
	c.calls++
	if c.during != nil {
		c.during()
	}
	return c.err
}
func (c *completeTVHintNativeTest) ListTorrents(context.Context, string) ([]*ports.TorrentInfo, error) {
	err := c.call()
	return []*ports.TorrentInfo{{Progress: 1}}, err
}
func (c *completeTVHintNativeTest) GetTorrentMetainfo(context.Context, string) ([]byte, error) {
	err := c.call()
	return []byte("native metainfo"), err
}
func (c *completeTVHintNativeTest) GetTorrentFiles(context.Context, string) ([]ports.TorrentFile, error) {
	err := c.call()
	return []ports.TorrentFile{{}}, err
}

func completeTVHintFixture(t *testing.T) (*Manager, *completeTVHintReaderTest, ports.CompleteTVTrackingRequest) {
	t.Helper()
	base, err := os.MkdirTemp("/data/temp", "complete-tv-hint-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
		if _, err := os.Lstat(base); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("fixture remains: %s: %v", base, err)
		}
	})
	files := rootfs.NewFilesystem()
	identity := func(name string) (string, ports.FileIdentity) {
		p := filepath.Join(base, name)
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		r, err := files.OpenRoot(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		id, err := r.Identity(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return p, id
	}
	root, rootID := identity("library")
	source, sourceID := identity("downloads")
	if rootID.Inode == sourceID.Inode && rootID.Device == sourceID.Device {
		t.Fatal("fixture roots must be physically distinct")
	}
	if err := os.WriteFile(filepath.Join(root, ".pulsemedia_root"), []byte("marker"), 0600); err != nil {
		t.Fatal(err)
	}
	request := ports.CompleteTVTrackingRequest{Lease: ports.JobLease{ID: uuid.New(), Owner: "hint-test"}, OperationID: uuid.New(), TaskID: uuid.New()}
	e := &ports.CompleteTVTrackingHintEvidence{
		Digest:         strings.Repeat("a", 64),
		Receipt:        ports.CompleteTVTrackingHintReceipt{JobID: request.Lease.ID, OperationID: request.OperationID, TaskID: request.TaskID},
		Binding:        ports.DownloadWatchBinding{RootPath: root, LocalRoot: source, RootIdentity: rootID},
		SourceIdentity: sourceID,
	}
	return &Manager{trackingHintFiles: files}, &completeTVHintReaderTest{evidence: e}, request
}

func completeTVHintMutate(t *testing.T, r *completeTVHintReaderTest, change string) {
	t.Helper()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	b := r.evidence.Binding
	switch change {
	case "root-replaced", "source-replaced", "root-moved":
		p := b.RootPath
		if change == "source-replaced" {
			p = b.LocalRoot
		}
		check(os.Rename(p, p+"-old"))
		if change != "root-moved" {
			check(os.Mkdir(p, 0700))
			// Even a valid marker cannot authorize a replacement directory.
			if change == "root-replaced" {
				check(os.WriteFile(filepath.Join(p, ".pulsemedia_root"), []byte("marker"), 0600))
			}
		}
	case "marker-missing", "marker-symlink":
		marker := filepath.Join(b.RootPath, ".pulsemedia_root")
		check(os.Remove(marker))
		if change == "marker-symlink" {
			target := filepath.Join(b.LocalRoot, "marker")
			check(os.WriteFile(target, []byte("marker"), 0600))
			check(os.Symlink(target, marker))
		}
	case "authority":
		r.evidence.Digest = strings.Repeat("b", 64)
	case "lease":
		r.err = ports.ErrCompleteTVObservationStale
	case "evidence-removed":
		r.evidence = nil
	default:
		t.Fatalf("unknown mutation %q", change)
	}
}

// Exercise the exact three wrappers used by ObserveCompleteTVTracking. Rejected
// fences must discard even a successful native payload, rather than infer absence.
func completeTVHintCall(c completeTVHintReadClient, ctx context.Context, api string) (bool, error) {
	switch api {
	case "list":
		v, err := c.ListTorrents(ctx, "operation-tag")
		return v != nil, err
	case "metainfo":
		v, err := c.GetTorrentMetainfo(ctx, "infohash")
		return v != nil, err
	default:
		v, err := c.GetTorrentFiles(ctx, "infohash")
		return v != nil, err
	}
}

func TestCompleteTVHintNativeFences(t *testing.T) {
	for _, api := range []string{"list", "metainfo", "files"} {
		t.Run(api, func(t *testing.T) {
			for _, change := range []string{"success", "native-failure", "root-replaced", "source-replaced", "root-moved", "marker-missing", "marker-symlink", "authority", "lease", "evidence-removed"} {
				phases := []string{"before", "during"}
				if change == "success" || change == "native-failure" {
					phases = []string{"during"}
				}
				for _, phase := range phases {
					t.Run(change+"/"+phase, func(t *testing.T) {
						m, reader, request := completeTVHintFixture(t)
						probe, err := m.completeTVHintProbe(context.Background(), reader, request)
						if err != nil {
							t.Fatal(err)
						}
						native := &completeTVHintNativeTest{}
						want := ports.ErrCompleteTVObservationStale
						switch change {
						case "success":
							want = nil
						case "native-failure":
							want = ports.ErrUnavailable
							native.err = want
						default:
							mutate := func() { completeTVHintMutate(t, reader, change) }
							if phase == "before" {
								mutate()
							} else {
								native.during = mutate
							}
						}
						present, err := completeTVHintCall(completeTVHintReadClient{native, probe}, context.Background(), api)
						if !errors.Is(err, want) {
							t.Fatalf("error = %v, want %v", err, want)
						}
						if want == nil && !present {
							t.Fatal("successful payload lost")
						}
						if errors.Is(want, ports.ErrCompleteTVObservationStale) && present {
							t.Fatal("stale native payload escaped")
						}
						calls, reads := 1, 3
						if phase == "before" {
							calls, reads = 0, 2
						}
						if native.calls != calls || reader.reads != reads || reader.lease != request.Lease {
							t.Fatalf("calls=%d reads=%d lease=%+v", native.calls, reader.reads, reader.lease)
						}
					})
				}
			}
		})
	}
}

func TestCompleteTVHintCancelledNativeWaitsForReturn(t *testing.T) {
	for _, api := range []string{"list", "metainfo", "files"} {
		t.Run(api, func(t *testing.T) {
			m, reader, request := completeTVHintFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			probe, err := m.completeTVHintProbe(ctx, reader, request)
			if err != nil {
				t.Fatal(err)
			}
			entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
			type result struct {
				present bool
				err     error
			}
			done := make(chan result, 1)
			native := &completeTVHintNativeTest{during: func() { close(entered); <-release }}
			go func() {
				defer close(exited)
				present, err := completeTVHintCall(completeTVHintReadClient{native, probe}, context.WithoutCancel(ctx), api)
				done <- result{present, err}
			}()
			defer func() {
				close(release)
				select {
				case <-exited:
				case <-time.After(3 * time.Second):
					t.Error("owned native goroutine did not exit")
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("native did not start")
			}
			cancel()
			select {
			case got := <-done:
				t.Fatalf("returned before physical completion: %+v", got)
			case <-time.After(30 * time.Millisecond):
			}
			select {
			case release <- struct{}{}:
			case <-time.After(3 * time.Second):
				t.Fatal("native did not accept release")
			}
			select {
			case got := <-done:
				if got.present || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("late result escaped cancellation: %+v", got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("native return was not observed")
			}
			if err := probe(context.WithoutCancel(ctx)); !errors.Is(err, context.Canceled) {
				t.Fatalf("writeback escaped original cancellation: %v", err)
			}
		})
	}
}

func TestCompleteTVHintWritebackProbe(t *testing.T) {
	for _, change := range []string{"root-replaced", "source-replaced", "authority", "lease", "marker-symlink"} {
		t.Run(change, func(t *testing.T) {
			m, reader, request := completeTVHintFixture(t)
			probe, err := m.completeTVHintProbe(context.Background(), reader, request)
			if err != nil {
				t.Fatal(err)
			}
			native := &completeTVHintNativeTest{}
			client := completeTVHintReadClient{native, probe}
			for _, api := range []string{"list", "metainfo", "files", "list"} {
				if present, err := completeTVHintCall(client, context.Background(), api); err != nil || !present {
					t.Fatalf("%s: %v", api, err)
				}
			}
			completeTVHintMutate(t, reader, change)
			if err := probe(context.WithoutCancel(context.Background())); !errors.Is(err, ports.ErrCompleteTVObservationStale) {
				t.Fatalf("writeback fence = %v", err)
			}
			if native.calls != 4 {
				t.Fatalf("native calls = %d", native.calls)
			}
		})
	}
}

func TestCompleteTVHintEvidenceAdmission(t *testing.T) {
	for _, change := range []string{"no-filesystem", "empty-digest", "job", "operation", "task", "reader-error"} {
		t.Run(change, func(t *testing.T) {
			m, reader, request := completeTVHintFixture(t)
			want := ports.ErrCompleteTVObservationStale
			switch change {
			case "no-filesystem":
				m.trackingHintFiles = nil
				want = ports.ErrUnavailable
			case "empty-digest":
				reader.evidence.Digest = ""
			case "job":
				reader.evidence.Receipt.JobID = uuid.New()
			case "operation":
				reader.evidence.Receipt.OperationID = uuid.New()
			case "task":
				reader.evidence.Receipt.TaskID = uuid.New()
			case "reader-error":
				reader.err = ports.ErrUnavailable
				want = reader.err
			}
			probe, err := m.completeTVHintProbe(context.Background(), reader, request)
			if probe != nil || !errors.Is(err, want) {
				t.Fatalf("admission = %v, probe nil=%v", err, probe == nil)
			}
		})
	}
}

func TestCompleteTVHintPeriodicUnchanged(t *testing.T) {
	for _, optionalReader := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-reader", true: "nil-evidence"}[optionalReader], func(t *testing.T) {
			reader := &completeTVHintReaderTest{}
			repository := reader.CompleteTVTrackingRepository
			if optionalReader {
				repository = reader
			}
			m := &Manager{}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			request := ports.CompleteTVTrackingRequest{}
			snapshot := ports.CompleteTVTrackingSnapshot{Request: request, Seal: "periodic-seal"}
			frozen := snapshot
			probe, err := m.completeTVHintProbe(ctx, repository, request)
			if err != nil {
				t.Fatal(err)
			}
			if err := probe(context.WithoutCancel(ctx)); err != nil {
				t.Fatalf("periodic writeback behavior changed: %v", err)
			}
			if !reflect.TypeFor[ports.CompleteTVTrackingSnapshot]().Comparable() || snapshot != frozen {
				t.Fatal("snapshot comparison changed")
			}
			native := &completeTVHintNativeTest{}
			for _, api := range []string{"list", "metainfo", "files"} {
				if present, err := completeTVHintCall(completeTVHintReadClient{native, probe}, context.WithoutCancel(ctx), api); err != nil || !present {
					t.Fatalf("periodic %s changed: present=%v err=%v", api, present, err)
				}
			}
			if native.calls != 3 {
				t.Fatalf("periodic native calls=%d", native.calls)
			}
			wantReads := 0
			if optionalReader {
				wantReads = 1
			}
			if reader.reads != wantReads {
				t.Fatalf("periodic reads=%d", reader.reads)
			}
		})
	}
}
