package subtitle

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var errLease = errors.New("lease expired")

const chineseSRT = "1\n00:00:01,000 --> 00:00:02,000\n你好\n\n"

type fakeRepo struct {
	snapshot    ports.MediaSidecarSnapshot
	invalid     bool
	validations int
	onValidate  func(ports.MediaSidecarSnapshot)
	completion  *ports.MediaSubtitleCompletion
	review      bool
	done        bool
	onComplete  func()
}

func (r *fakeRepo) LoadSubtitle(context.Context, ports.JobLease) (ports.MediaSidecarSnapshot, error) {
	if r.done {
		return ports.MediaSidecarSnapshot{}, errLease
	}
	return r.snapshot, nil
}
func (r *fakeRepo) ValidateSubtitle(_ context.Context, _ ports.JobLease, s ports.MediaSidecarSnapshot) error {
	r.validations++
	if r.invalid || r.done {
		return errLease
	}
	if !reflect.DeepEqual(s, r.snapshot) {
		return ports.ErrMediaSidecarStale
	}
	if r.onValidate != nil {
		r.onValidate(s)
	}
	return nil
}
func (r *fakeRepo) CompleteSubtitle(ctx context.Context, l ports.JobLease, s ports.MediaSidecarSnapshot, c ports.MediaSubtitleCompletion) error {
	if err := r.ValidateSubtitle(ctx, l, s); err != nil {
		return err
	}
	if r.onComplete != nil {
		r.onComplete()
	}
	r.completion = &c
	r.review = c.Outcome == "observed" && publicationError(c.Publication, s.RootIdentity.Device) != nil
	r.done = true
	return nil
}

type fakeRoot struct {
	files     *fakeFiles
	namespace bool
}

func (r *fakeRoot) Identity(context.Context) (ports.FileIdentity, error) {
	id := r.files.repo.snapshot.RootIdentity
	if r.namespace && r.files.replaceNamespace {
		id.Inode++
	}
	return id, nil
}
func (r *fakeRoot) Inspect(_ context.Context, name string) (ports.FileIdentity, error) {
	f := r.files
	if name == ".pulsemedia_root" {
		if f.markerMissing {
			return ports.FileIdentity{}, fs.ErrNotExist
		}
		return ports.FileIdentity{Device: 1, Inode: 99}, nil
	}
	if name == f.repo.snapshot.FileRelative {
		id := f.repo.snapshot.FileIdentity
		if f.replaceSource {
			id.Inode++
		}
		return id, nil
	}
	if name != "unexpected layout/actual-video.zh-cn.default.srt" {
		return ports.FileIdentity{}, ports.ErrFileEscape
	}
	if f.inspectErr != nil {
		return ports.FileIdentity{}, f.inspectErr
	}
	if f.existing != nil {
		return *f.existing, nil
	}
	return ports.FileIdentity{}, fs.ErrNotExist
}
func (*fakeRoot) Close() {}

type fakeFiles struct {
	repo                                                             *fakeRepo
	released                                                         atomic.Bool
	locked                                                           bool
	replaceNamespace, replaceSource, replaceDirectory, markerMissing bool
	hashCalls, publishCalls                                          int
	existing                                                         *ports.FileIdentity
	inspectErr                                                       error
	hash                                                             ports.SubtitleVideoHash
	hashErr, publishErr                                              error
	result                                                           ports.SidecarResult
	request                                                          ports.SidecarRequest
	onLock, onHash, onPublish                                        func()
}

func (f *fakeFiles) OpenRoot(context.Context, string) (ports.TransferRoot, error) {
	return &fakeRoot{files: f, namespace: true}, nil
}
func (f *fakeFiles) LockRoots(_ context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	f.locked = true
	if f.onLock != nil {
		f.onLock()
	}
	return map[string]ports.TransferRoot{paths[0]: &fakeRoot{files: f}}, func() { f.released.Store(true) }, nil
}
func (f *fakeFiles) InspectSidecarDirectory(_ context.Context, _ ports.TransferRoot, relative string) (ports.SidecarDirectoryIdentity, error) {
	if relative != "unexpected layout" {
		return ports.SidecarDirectoryIdentity{}, ports.ErrFileIdentity
	}
	id := ports.SidecarDirectoryIdentity{Device: 1, Inode: 8}
	if f.replaceDirectory {
		id.Inode++
	}
	return id, nil
}
func (f *fakeFiles) HashSubtitleVideo(_ context.Context, _ ports.TransferRoot, relative string, id ports.FileIdentity) (ports.SubtitleVideoHash, error) {
	f.hashCalls++
	if relative != f.repo.snapshot.FileRelative || id != f.repo.snapshot.FileIdentity || f.released.Load() {
		return ports.SubtitleVideoHash{}, ports.ErrFileIdentity
	}
	if f.onHash != nil {
		f.onHash()
	}
	return f.hash, f.hashErr
}
func (f *fakeFiles) PublishSubtitle(_ context.Context, _ ports.TransferRoot, r ports.SidecarRequest) (ports.SidecarResult, error) {
	f.publishCalls++
	f.request = r
	if f.onPublish != nil {
		f.onPublish()
	}
	return f.result, f.publishErr
}

type fakeProvider struct {
	files  *fakeFiles
	calls  int
	body   []byte
	err    error
	onFind func()
}

func (p *fakeProvider) FindChineseSubtitle(_ context.Context, h ports.SubtitleVideoHash) ([]byte, error) {
	p.calls++
	if h != p.files.hash || p.files.released.Load() {
		return nil, ports.ErrFileIdentity
	}
	if p.onFind != nil {
		p.onFind()
	}
	return p.body, p.err
}
func fixture() (*Service, *fakeRepo, *fakeFiles, *fakeProvider) {
	unit, mediaID := uuid.New(), uuid.New()
	r := &fakeRepo{snapshot: ports.MediaSidecarSnapshot{
		Binding: "trusted-binding", Source: ports.MediaSidecarPayload{Version: 1, SourceEventID: uuid.New(), FileID: uuid.New(), UnitID: unit},
		RootID: uuid.New(), RootPath: "/library", RootMediaType: "movie", RootHealth: "online", RootGeneration: 1, RootHealthRevision: 1,
		RootIdentity: ports.FileIdentity{Device: 1, Inode: 2}, FileRelative: "unexpected layout/actual-video.mkv", FileFingerprint: "trusted-fingerprint",
		FileIdentity: ports.FileIdentity{Device: 1, Inode: 3, Size: 100000, Mtime: 42}, FileMappingRevision: 1,
		MediaID: mediaID, MediaType: "movie", Title: "Title deliberately unrelated to directory", MediaMetadataVersion: 1, UnitMetadataVersion: 1, UnitFactsVersion: 1, UnitKey: "movie",
		Mappings: []ports.MediaSidecarMapping{{UnitID: unit, MediaID: mediaID, UnitKey: "movie", MetadataVersion: 1}},
	}}
	f := &fakeFiles{repo: r, hash: ports.SubtitleVideoHash{Hash: strings.Repeat("a", 32) + ";" + strings.Repeat("b", 32) + ";" + strings.Repeat("c", 32) + ";" + strings.Repeat("d", 32), SizeBytes: 100000}, result: ports.SidecarResult{Disposition: ports.SidecarCreated, Published: true, Identity: ports.FileIdentity{Device: 1, Inode: 10, Size: 48}}}
	p := &fakeProvider{files: f, body: []byte(chineseSRT)}
	return NewService(r, f, p), r, f, p
}
func TestExecuteOutcomes(t *testing.T) {
	for _, scenario := range []string{"created", "existing", "too_small", "not_found", "empty", "malformed", "english", "unsafe", "provider_error", "hash_error", "bad_hash", "wrong_size", "uncertain", "unsafe_publication", "invalid_publication"} {
		t.Run(scenario, func(t *testing.T) {
			s, r, f, p := fixture()
			wantErr := false
			switch scenario {
			case "existing":
				id := ports.FileIdentity{Device: 1, Inode: 55, Size: 15}
				f.existing = &id
			case "too_small":
				r.snapshot.FileIdentity.Size = ports.MinSubtitleVideoBytes - 1
			case "not_found":
				p.body = nil
			case "empty":
				p.body = []byte{}
				wantErr = true
			case "malformed":
				p.body = []byte("你好")
				wantErr = true
			case "english":
				p.body = []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")
				wantErr = true
			case "unsafe":
				f.inspectErr = ports.ErrFileSymlink
				wantErr = true
			case "provider_error":
				p.err = errors.New("upstream unknown")
				wantErr = true
			case "hash_error":
				f.hashErr = ports.ErrFileIdentity
				wantErr = true
			case "bad_hash":
				f.hash.Hash = "invalid"
				wantErr = true
			case "wrong_size":
				f.hash.SizeBytes++
				wantErr = true
			case "uncertain":
				f.result.Disposition = ports.SidecarUncertain
				f.publishErr = errors.New("fsync uncertain")
				wantErr = true
			case "unsafe_publication":
				f.result = ports.SidecarResult{Disposition: ports.SidecarUnsafeExisting}
				wantErr = true
			case "invalid_publication":
				f.result.Identity = ports.FileIdentity{}
				wantErr = true
			}
			r.onComplete = func() {
				if f.released.Load() {
					t.Error("slot released before completion")
				}
			}
			err := s.Execute(context.Background(), ports.JobLease{})
			if (err != nil) != wantErr {
				t.Fatalf("error=%v wantErr=%v", err, wantErr)
			}
			if !f.released.Load() {
				t.Fatal("slot not released")
			}
			switch scenario {
			case "existing", "too_small":
				if f.hashCalls != 0 || p.calls != 0 || f.publishCalls != 0 {
					t.Fatal("unnecessary hash/network/publication")
				}
				if r.completion == nil || r.completion.VideoHash != (ports.SubtitleVideoHash{}) {
					t.Fatal("unexpected hash")
				}
				if scenario == "existing" && (r.completion.Publication.Identity != *f.existing || r.completion.Publication.Published || r.completion.Publication.Disposition != ports.SidecarPreservedRegular) {
					t.Fatal("existing ownership claimed")
				}
				if scenario == "too_small" && (r.completion.Outcome != "too_small" || r.completion.Name != "" || r.completion.Publication != (ports.SidecarResult{})) {
					t.Fatal("invalid too-small completion")
				}
			case "not_found":
				if r.completion == nil || r.completion.Outcome != "not_found" || r.completion.VideoHash != f.hash || r.completion.Name != "" || r.completion.Publication != (ports.SidecarResult{}) || f.publishCalls != 0 {
					t.Fatal("invalid empty lookup")
				}
			case "created":
				if r.completion == nil || r.completion.Outcome != "observed" || r.completion.Name != "unexpected layout/actual-video.zh-cn.default.srt" || r.completion.VideoHash != f.hash || r.completion.Publication != f.result {
					t.Fatal("missing actual completion")
				}
				if f.request.DestinationDirectory != "unexpected layout" || f.request.Name != "actual-video.zh-cn.default.srt" || string(f.request.Content) != chineseSRT || f.request.SourceIdentity != r.snapshot.FileIdentity || f.request.DestinationIdentity != (ports.SidecarDirectoryIdentity{Device: 1, Inode: 8}) {
					t.Fatalf("incorrect request: %+v", f.request)
				}
			case "uncertain", "unsafe_publication", "invalid_publication":
				if !r.review || r.completion == nil || r.completion.Publication != f.result {
					t.Fatal("actual review outcome lost")
				}
				if err := s.Execute(context.Background(), ports.JobLease{}); err == nil || f.publishCalls != 1 {
					t.Fatal("review replayed")
				}
			default:
				if r.completion != nil || f.publishCalls != 0 {
					t.Fatal("invalid result completed")
				}
			}
		})
	}
}

func TestLeaseAndEvidenceFences(t *testing.T) {
	for _, phase := range []string{"waiting", "before_hash", "after_hash", "before_network", "after_network", "before_publication", "after_publication"} {
		t.Run(phase, func(t *testing.T) {
			s, r, f, p := fixture()
			switch phase {
			case "waiting":
				f.onLock = func() { r.invalid = true }
			case "before_hash":
				r.onValidate = func(ports.MediaSidecarSnapshot) {
					if f.locked {
						r.invalid = true
					}
				}
			case "after_hash", "before_network":
				f.onHash = func() { r.invalid = true }
			case "after_network", "before_publication":
				p.onFind = func() { r.invalid = true }
			case "after_publication":
				f.onPublish = func() { r.invalid = true }
			}
			if err := s.Execute(context.Background(), ports.JobLease{}); !errors.Is(err, errLease) {
				t.Fatalf("error=%v", err)
			}
			if r.completion != nil {
				t.Fatal("completed lost lease")
			}
			if phase != "after_publication" && f.publishCalls != 0 {
				t.Fatal("publication crossed fence")
			}
			if !f.released.Load() {
				t.Fatal("slot leaked")
			}
		})
	}
	for _, change := range []string{"namespace", "source", "directory", "marker"} {
		for _, phase := range []string{"hash", "network", "publication"} {
			t.Run(change+"/"+phase, func(t *testing.T) {
				s, r, f, p := fixture()
				replace := func() {
					switch change {
					case "namespace":
						f.replaceNamespace = true
					case "source":
						f.replaceSource = true
					case "directory":
						f.replaceDirectory = true
					case "marker":
						f.markerMissing = true
					}
				}
				switch phase {
				case "hash":
					f.onHash = replace
				case "network":
					p.onFind = replace
				case "publication":
					f.onPublish = replace
				}
				if err := s.Execute(context.Background(), ports.JobLease{}); err == nil {
					t.Fatal("replacement accepted")
				}
				if r.completion != nil {
					t.Fatal("replacement completed")
				}
				if phase != "publication" && f.publishCalls != 0 {
					t.Fatal("published after replacement")
				}
			})
		}
	}
}

func TestCancelWaitsForPhysicalReturn(t *testing.T) {
	for _, phase := range []string{"hash", "network", "publication"} {
		t.Run(phase, func(t *testing.T) {
			s, r, f, p := fixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, unblock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			block := func() { close(entered); <-unblock }
			switch phase {
			case "hash":
				f.onHash = block
			case "network":
				p.onFind = block
			case "publication":
				f.onPublish = block
			}
			go func() { done <- s.Execute(ctx, ports.JobLease{}) }()
			defer func() {
				close(unblock)
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Errorf("error=%v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("I/O did not finish")
				}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("I/O not entered")
			}
			cancel()
			if f.released.Load() {
				t.Fatal("released while I/O running")
			}
			select {
			case <-done:
				t.Fatal("returned before I/O returned")
			default:
			}
			// completion is inspected only after the goroutine has joined below.
			t.Cleanup(func() {
				if r.completion != nil {
					t.Error("late success completed")
				}
				if !f.released.Load() {
					t.Error("slot leaked")
				}
			})
		})
	}
}

func TestRejectUnsafeSnapshot(t *testing.T) {
	mutations := map[string]func(*ports.MediaSidecarSnapshot){
		"binding":   func(s *ports.MediaSidecarSnapshot) { s.Binding = "" },
		"event":     func(s *ports.MediaSidecarSnapshot) { s.Source.SourceEventID = uuid.Nil },
		"file":      func(s *ports.MediaSidecarSnapshot) { s.Source.FileID = uuid.Nil },
		"root":      func(s *ports.MediaSidecarSnapshot) { s.RootID = uuid.Nil },
		"offline":   func(s *ports.MediaSidecarSnapshot) { s.RootHealth = "offline" },
		"path":      func(s *ports.MediaSidecarSnapshot) { s.FileRelative = "../escape.mkv" },
		"root_path": func(s *ports.MediaSidecarSnapshot) { s.RootPath = "/library/../library" },
		"identity":  func(s *ports.MediaSidecarSnapshot) { s.FileIdentity.Device = 2 },
		"marker":    func(s *ports.MediaSidecarSnapshot) { s.MarkerFilename = "../marker" },
		"mapping":   func(s *ports.MediaSidecarSnapshot) { s.Mappings[0].MediaID = uuid.New() },
		"unit":      func(s *ports.MediaSidecarSnapshot) { s.UnitKey = "other" },
		"duplicate": func(s *ports.MediaSidecarSnapshot) { s.Mappings = append(s.Mappings, s.Mappings[0]) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s, r, f, p := fixture()
			mutate(&r.snapshot)
			if err := s.Execute(context.Background(), ports.JobLease{}); err == nil {
				t.Fatal("unsafe snapshot accepted")
			}
			if f.locked || p.calls != 0 || r.completion != nil {
				t.Fatal("unsafe authority used")
			}
		})
	}
}

func TestSnapshotCopies(t *testing.T) {
	s, r, _, _ := fixture()
	season, episode, year := int32(1), int32(2), int32(2020)
	r.snapshot.MediaType = "tv"
	r.snapshot.RootMediaType = "tv"
	r.snapshot.UnitKey = "S01E02"
	r.snapshot.SeasonNumber = &season
	r.snapshot.EpisodeNumber = &episode
	r.snapshot.Year = &year
	r.snapshot.Mappings[0].UnitKey = "S01E02"
	r.snapshot.Mappings[0].SeasonNumber = &season
	r.snapshot.Mappings[0].EpisodeNumber = &episode
	original := cloneSnapshot(r.snapshot)
	r.onValidate = func(s ports.MediaSidecarSnapshot) {
		*s.Year = 999
		*s.SeasonNumber = 999
		*s.Mappings[0].EpisodeNumber = 999
		s.Mappings[0].UnitKey = "changed"
	}
	if err := s.Execute(context.Background(), ports.JobLease{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, r.snapshot) {
		t.Fatal("dependency mutated authority")
	}
}

func TestProviderBytesCopiedBeforeValidation(t *testing.T) {
	s, r, f, p := fixture()
	r.onValidate = func(ports.MediaSidecarSnapshot) {
		if p.calls > 0 {
			for i := range p.body {
				p.body[i] = 'x'
			}
		}
	}
	if err := s.Execute(context.Background(), ports.JobLease{}); err != nil {
		t.Fatal(err)
	}
	if string(f.request.Content) != chineseSRT {
		t.Fatal("provider bytes changed before publication")
	}
}
