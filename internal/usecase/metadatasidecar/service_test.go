package metadatasidecar_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/usecase/metadatasidecar"
	"github.com/google/uuid"
)

var errInjected = errors.New("injected errInjected")

func number(n int32) *int32 { return &n }

func movieSnapshot() ports.MediaSidecarSnapshot {
	mediaID, unitID := uuid.New(), uuid.New()
	return ports.MediaSidecarSnapshot{
		Binding: "frozen", Source: ports.MediaSidecarPayload{Version: 1, SourceEventID: uuid.New(), FileID: uuid.New(), UnitID: unitID},
		RootID: uuid.New(), RootPath: "/library", RootMediaType: "movie", RootHealth: "online", RootGeneration: 2,
		RootIdentity: ports.FileIdentity{Device: 10, Inode: 20},
		FileRelative: "Canonical (2024)/feature.mkv", FileIdentity: ports.FileIdentity{Device: 10, Inode: 30, Size: 123, Mtime: 456},
		FileFingerprint: "fingerprint", FileMappingRevision: 3, FileMutationSequence: 4,
		MediaID: mediaID, MediaType: "movie", Title: "Canonical", OriginalTitle: "Original", Year: number(2024), TMDBID: 77,
		MediaMetadataVersion: 5, UnitMetadataVersion: 6, UnitFactsVersion: 7, UnitKey: "movie",
		Mappings: []ports.MediaSidecarMapping{{UnitID: unitID, MediaID: mediaID, UnitKey: "movie", MetadataVersion: 6}},
	}
}

func tvSnapshot() ports.MediaSidecarSnapshot {
	s := movieSnapshot()
	s.MediaType, s.RootMediaType, s.UnitKey = "tv", "tv", "S03E07"
	s.SeasonNumber, s.EpisodeNumber = number(3), number(7)
	s.FileRelative = "Canonical (2024)/Season 03/episode.mkv"
	s.Mappings[0].UnitKey, s.Mappings[0].SeasonNumber, s.Mappings[0].EpisodeNumber = s.UnitKey, number(3), number(7)
	return s
}

type repository struct {
	t                    *testing.T
	ctx                  context.Context
	lease                ports.JobLease
	snapshot             ports.MediaSidecarSnapshot
	events               *[]string
	loadErr, completeErr error
	validate             func(int, ports.MediaSidecarSnapshot) error
	validations          int
	completion           *ports.MediaSidecarCompletion
	completedSnapshot    ports.MediaSidecarSnapshot
	completeHook         func()
}

func (r *repository) authority(ctx context.Context, lease ports.JobLease) {
	r.t.Helper()
	if ctx != r.ctx || lease != r.lease {
		r.t.Error("context or lease replaced")
	}
}
func (r *repository) Load(ctx context.Context, lease ports.JobLease) (ports.MediaSidecarSnapshot, error) {
	r.authority(ctx, lease)
	*r.events = append(*r.events, "load")
	return r.snapshot, r.loadErr
}
func (r *repository) Validate(ctx context.Context, lease ports.JobLease, s ports.MediaSidecarSnapshot) error {
	r.authority(ctx, lease)
	*r.events = append(*r.events, "validate")
	r.validations++
	if r.validate != nil {
		return r.validate(r.validations, s)
	}
	return nil
}
func (r *repository) Complete(ctx context.Context, lease ports.JobLease, s ports.MediaSidecarSnapshot, c ports.MediaSidecarCompletion) error {
	r.authority(ctx, lease)
	*r.events = append(*r.events, "complete")
	r.completedSnapshot, r.completion = s, &c
	if r.completeHook != nil {
		r.completeHook()
	}
	return r.completeErr
}

type root struct {
	id       ports.FileIdentity
	identity func() (ports.FileIdentity, error)
	inspect  func(string) (ports.FileIdentity, error)
	closed   int
}

func (r *root) Identity(context.Context) (ports.FileIdentity, error) {
	if r.identity != nil {
		return r.identity()
	}
	return r.id, nil
}
func (r *root) Inspect(_ context.Context, path string) (ports.FileIdentity, error) {
	return r.inspect(path)
}
func (r *root) Close() { r.closed++ }

type filesystem struct {
	t                 *testing.T
	events            *[]string
	pinned, namespace *root
	dir               ports.SidecarDirectoryIdentity
	requests          []ports.SidecarRequest
	lockHook          func()
	openHook          func() error
	dirHook           func() (ports.SidecarDirectoryIdentity, error)
	publish           func(ports.SidecarRequest) (ports.SidecarResult, error)
	released          atomic.Bool
	locked            bool
}

func (f *filesystem) LockRoots(_ context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	*f.events = append(*f.events, "lock")
	if len(paths) != 1 || paths[0] != "/library" {
		f.t.Error("wrong lock roots")
	}
	if f.lockHook != nil {
		f.lockHook()
	}
	f.locked = true
	return map[string]ports.TransferRoot{"/library": f.pinned}, func() { f.released.Store(true); *f.events = append(*f.events, "release") }, nil
}
func (f *filesystem) OpenRoot(_ context.Context, path string) (ports.TransferRoot, error) {
	*f.events = append(*f.events, "open")
	if path != "/library" || !f.locked || f.released.Load() {
		f.t.Error("namespace opened outside physical slot")
	}
	if f.openHook != nil {
		if err := f.openHook(); err != nil {
			return nil, err
		}
	}
	return f.namespace, nil
}
func (f *filesystem) InspectSidecarDirectory(_ context.Context, r ports.TransferRoot, path string) (ports.SidecarDirectoryIdentity, error) {
	*f.events = append(*f.events, "directory")
	if r != f.pinned || path != "Canonical (2024)" && path != "Canonical" {
		f.t.Errorf("unproven destination %q", path)
	}
	if f.dirHook != nil {
		return f.dirHook()
	}
	return f.dir, nil
}
func (f *filesystem) PublishSidecar(_ context.Context, r ports.TransferRoot, request ports.SidecarRequest) (ports.SidecarResult, error) {
	if r != f.pinned || f.released.Load() || !f.locked {
		f.t.Error("publish outside slot")
	}
	if len(*f.events) == 0 || (*f.events)[len(*f.events)-1] != "validate" {
		f.t.Error("Validate must immediately precede each effect")
	}
	*f.events = append(*f.events, "publish:"+request.Name)
	stored := request
	stored.Content = bytes.Clone(request.Content)
	f.requests = append(f.requests, stored)
	if f.publish != nil {
		return f.publish(request)
	}
	outcome := created()
	outcome.Identity.Inode += int64(len(f.requests) - 1)
	return outcome, nil
}
func created() ports.SidecarResult {
	return ports.SidecarResult{Disposition: ports.SidecarCreated, Published: true, Identity: ports.FileIdentity{Device: 10, Inode: 50, Size: 12, Mtime: 99}}
}

type metadata struct {
	movie func(context.Context, int64, string) (*ports.MovieMetadata, error)
	tv    func(context.Context, int64, string) (*ports.TVDetails, error)
}

func (m metadata) GetMovieDetails(c context.Context, id int64, language string) (*ports.MovieMetadata, error) {
	return m.movie(c, id, language)
}
func (m metadata) GetTVDetails(c context.Context, id int64, language string) (*ports.TVDetails, error) {
	return m.tv(c, id, language)
}

type artwork func(context.Context, string) ([]byte, error)

func (a artwork) FetchPoster(ctx context.Context, path string) ([]byte, error) { return a(ctx, path) }

type fixture struct {
	t      *testing.T
	ctx    context.Context
	lease  ports.JobLease
	events []string
	repo   *repository
	fs     *filesystem
}

func setup(t *testing.T, snapshot ports.MediaSidecarSnapshot) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), lease: ports.JobLease{ID: uuid.New(), Owner: "worker", Token: 8}}
	f.repo = &repository{t: t, snapshot: snapshot, ctx: f.ctx, lease: f.lease, events: &f.events}
	inspect := func(path string) (ports.FileIdentity, error) {
		if path == ".pulsemedia_root" || path == "custom.marker" {
			return ports.FileIdentity{Device: 10, Inode: 21}, nil
		}
		if path == snapshot.FileRelative {
			return snapshot.FileIdentity, nil
		}
		return ports.FileIdentity{}, errInjected
	}
	f.fs = &filesystem{t: t, events: &f.events, dir: ports.SidecarDirectoryIdentity{Device: 10, Inode: 40}, pinned: &root{id: snapshot.RootIdentity, inspect: inspect}, namespace: &root{id: snapshot.RootIdentity, inspect: inspect}}
	return f
}
func (f *fixture) execute(m ports.MediaSidecarMetadata, a ports.MetadataArtworkFetcher) error {
	f.repo.ctx = f.ctx
	return metadatasidecar.NewService(f.repo, f.fs, m, a).Execute(f.ctx, f.lease)
}
func (f *fixture) noEffects() {
	f.t.Helper()
	if len(f.fs.requests) != 0 || f.repo.completion != nil {
		f.t.Fatal("unexpected effects", f.events)
	}
}
func (f *fixture) nfo() string {
	f.t.Helper()
	if len(f.fs.requests) == 0 {
		f.t.Fatal("missing NFO", f.events)
	}
	return string(f.fs.requests[0].Content)
}

func TestCanonicalFoldersAndActualCompletion(t *testing.T) {
	for _, kind := range []string{"movie", "tv", "sparse", "multimapping", "specials", "unknown_year"} {
		t.Run(kind, func(t *testing.T) {
			s := movieSnapshot()
			if kind != "movie" && kind != "unknown_year" {
				s = tvSnapshot()
			}
			switch kind {
			case "sparse":
				s.EpisodeNumber, s.Mappings[0].EpisodeNumber, s.UnitKey, s.Mappings[0].UnitKey = number(99), number(99), "S03E99", "S03E99"
			case "multimapping":
				s.Mappings = append(s.Mappings, ports.MediaSidecarMapping{UnitID: uuid.New(), MediaID: s.MediaID, UnitKey: "S03E09", SeasonNumber: number(3), EpisodeNumber: number(9)})
			case "specials":
				s.SeasonNumber, s.Mappings[0].SeasonNumber, s.UnitKey, s.Mappings[0].UnitKey, s.FileRelative = number(0), number(0), "S00E07", "S00E07", "Canonical (2024)/Season 00/special.mkv"
			case "unknown_year":
				s.Year, s.FileRelative = nil, "Canonical/feature.mkv"
			}
			f := setup(t, s)
			if err := f.execute(nil, nil); err != nil {
				t.Fatal(err)
			}
			if len(f.fs.requests) != 1 || len(f.repo.completion.Artifacts) != 1 {
				t.Fatal("invented artifacts")
			}
			request := f.fs.requests[0]
			if request.DestinationDirectory == "." || strings.Contains(request.DestinationDirectory, "Season") || request.SourceRelative != s.FileRelative || request.SourceIdentity != s.FileIdentity || request.DestinationIdentity != f.fs.dir {
				t.Fatal("request lost frozen evidence", request)
			}
			wantName, wantXML := "movie.nfo", "movie"
			if s.MediaType == "tv" {
				wantName, wantXML = "tvshow.nfo", "tvshow"
			}
			var doc struct {
				XMLName xml.Name
				Title   string `xml:"title"`
				Year    int    `xml:"year"`
				ID      struct {
					Type    string `xml:"type,attr"`
					Default bool   `xml:"default,attr"`
					Value   int64  `xml:",chardata"`
				} `xml:"uniqueid"`
			}
			if err := xml.Unmarshal(request.Content, &doc); err != nil {
				t.Fatal(err)
			}
			if request.Name != wantName || doc.XMLName.Local != wantXML || doc.Title != s.Title || doc.ID.Value != s.TMDBID || doc.ID.Type != "tmdb" || !doc.ID.Default {
				t.Fatal("invalid canonical NFO", f.nfo())
			}
			if kind == "unknown_year" && strings.Contains(f.nfo(), "<year>") {
				t.Fatal("invented year")
			}
			if f.repo.completion.Artifacts[0].Result != created() || !reflect.DeepEqual(f.repo.completedSnapshot, s) {
				t.Fatal("completion lost evidence")
			}
			if !f.fs.released.Load() || f.events[len(f.events)-2] != "complete" || f.fs.namespace.closed == 0 {
				t.Fatal("slot not held through completion/close", f.events)
			}
		})
	}
}

func TestRejectUnprovenLayoutsBeforeNetworkOrFilesystem(t *testing.T) {
	cases := map[string]func(*ports.MediaSidecarSnapshot){
		"root":                  func(s *ports.MediaSidecarSnapshot) { s.FileRelative = "feature.mkv" },
		"custom":                func(s *ports.MediaSidecarSnapshot) { s.FileRelative = "custom/feature.mkv" },
		"nested":                func(s *ports.MediaSidecarSnapshot) { s.FileRelative = "shared/Canonical (2024)/feature.mkv" },
		"traversal":             func(s *ports.MediaSidecarSnapshot) { s.FileRelative = "Canonical (2024)/../feature.mkv" },
		"foreign_mapping":       func(s *ports.MediaSidecarSnapshot) { s.Mappings[0].MediaID = uuid.New() },
		"missing_mapping":       func(s *ports.MediaSidecarSnapshot) { s.Mappings = nil },
		"different_source_unit": func(s *ports.MediaSidecarSnapshot) { s.Source.UnitID = uuid.New() },
		"duplicate_mapping":     func(s *ports.MediaSidecarSnapshot) { s.Mappings = append(s.Mappings, s.Mappings[0]) },
		"wrong_type":            func(s *ports.MediaSidecarSnapshot) { s.RootMediaType = "tv" },
		"offline":               func(s *ports.MediaSidecarSnapshot) { s.RootHealth = "offline" },
		"missing_baseline":      func(s *ports.MediaSidecarSnapshot) { s.RootIdentity = ports.FileIdentity{} },
		"foreign_device":        func(s *ports.MediaSidecarSnapshot) { s.FileIdentity.Device++ },
		"bad_marker":            func(s *ports.MediaSidecarSnapshot) { s.MarkerFilename = "../marker" },
		"missing_identity":      func(s *ports.MediaSidecarSnapshot) { s.TMDBID = 0 },
		"invalid_year":          func(s *ports.MediaSidecarSnapshot) { s.Year = number(-1) },
		"tv_root_episode": func(s *ports.MediaSidecarSnapshot) {
			*s = tvSnapshot()
			s.FileRelative = "Canonical (2024)/episode.mkv"
		},
		"tv_custom_season": func(s *ports.MediaSidecarSnapshot) {
			*s = tvSnapshot()
			s.FileRelative = "Canonical (2024)/Season 3/episode.mkv"
		},
		"tv_wrong_season": func(s *ports.MediaSidecarSnapshot) {
			*s = tvSnapshot()
			s.FileRelative = "Canonical (2024)/Season 02/episode.mkv"
		},
		"tv_no_numbering":           func(s *ports.MediaSidecarSnapshot) { *s = tvSnapshot(); s.SeasonNumber = nil },
		"tv_inconsistent_numbering": func(s *ports.MediaSidecarSnapshot) { *s = tvSnapshot(); s.Mappings[0].EpisodeNumber = number(8) },
		"tv_cross_season": func(s *ports.MediaSidecarSnapshot) {
			*s = tvSnapshot()
			s.Mappings = append(s.Mappings, ports.MediaSidecarMapping{UnitID: uuid.New(), MediaID: s.MediaID, UnitKey: "S04E01", SeasonNumber: number(4), EpisodeNumber: number(1)})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := movieSnapshot()
			change(&s)
			f := setup(t, s)
			if err := f.execute(nil, nil); err == nil {
				t.Fatal("unproven layout accepted")
			}
			f.noEffects()
			if !reflect.DeepEqual(f.events, []string{"load"}) {
				t.Fatal("layout rejection performed I/O", f.events)
			}
		})
	}
}

func TestMetadataAndArtworkDegradationAndIdentity(t *testing.T) {
	for _, mode := range []string{"verified", "metadata_error", "image_error", "wrong_id", "wrong_type", "nil_details", "bad_url", "credentials", "malformed_xml", "oversized_image", "current_poster", "invalid_current_poster"} {
		t.Run(mode, func(t *testing.T) {
			s := movieSnapshot()
			if mode == "current_poster" {
				s.PosterPath = "/current.jpg"
			}
			if mode == "invalid_current_poster" {
				s.PosterPath = "//foreign/secret.jpg"
			}
			f := setup(t, s)
			details := &ports.MovieMetadata{TMDBID: s.TMDBID, MediaType: "movie", Title: "Foreign title", Year: 1900, Overview: "Verified plot", IMDbID: "tt12345", Genres: []string{"Drama"}, PosterURL: "https://image.tmdb.org/t/p/w500/verified.jpg"}
			m := metadata{movie: func(ctx context.Context, id int64, lang string) (*ports.MovieMetadata, error) {
				f.events = append(f.events, "metadata")
				if f.fs.locked || ctx != f.ctx || id != s.TMDBID || lang == "" {
					t.Error("metadata after slot or wrong identity")
				}
				switch mode {
				case "metadata_error":
					return details, errInjected
				case "wrong_id":
					details.TMDBID++
				case "wrong_type":
					details.MediaType = "tv"
				case "nil_details":
					return nil, nil
				case "bad_url":
					details.PosterURL = "https://evil.example/poster.jpg?passkey=secret"
				case "credentials":
					details.Overview, details.IMDbID, details.Genres, details.PosterURL = "https://example/?passkey=secret", "https://secret", []string{"https://secret"}, ""
				case "malformed_xml":
					details.Overview = "invalid\x00text"
				case "invalid_current_poster":
					details.PosterURL = ""
				}
				return details, nil
			}}
			imageCalls := 0
			image := []byte{0xff, 0xd8, 0xff, 0xd9}
			a := artwork(func(_ context.Context, path string) ([]byte, error) {
				f.events = append(f.events, "artwork")
				imageCalls++
				want := "/verified.jpg"
				if mode == "current_poster" {
					want = "/current.jpg"
				}
				if f.fs.locked || path != want {
					t.Error("unsafe artwork target or slot ordering", path)
				}
				if mode == "image_error" {
					return image, errInjected
				}
				if mode == "oversized_image" {
					return make([]byte, ports.MaxSidecarBytes+1), nil
				}
				return image, nil
			})
			if err := f.execute(m, a); err != nil {
				t.Fatal(err)
			}
			wantArtifacts := 1
			if mode == "verified" || mode == "current_poster" || mode == "malformed_xml" {
				wantArtifacts = 2
			}
			if len(f.repo.completion.Artifacts) != wantArtifacts {
				t.Fatalf("artifacts=%d want=%d", len(f.repo.completion.Artifacts), wantArtifacts)
			}
			if strings.Contains(f.nfo(), "Foreign title") || strings.Contains(f.nfo(), "1900") || strings.Contains(f.nfo(), "secret") {
				t.Fatal("untrusted identity or credentials in NFO", f.nfo())
			}
			if mode == "verified" && (!strings.Contains(f.nfo(), "Verified plot") || !strings.Contains(f.nfo(), "tt12345") || !strings.Contains(f.nfo(), "Drama")) {
				t.Fatal("missing enrichment")
			}
			if mode == "wrong_id" || mode == "wrong_type" || mode == "metadata_error" {
				if imageCalls != 0 || strings.Contains(f.nfo(), "Verified plot") {
					t.Fatal("unverified details used")
				}
			}
		})
	}
}

func TestTVEnrichmentRequiresSameExplicitID(t *testing.T) {
	for _, mode := range []string{"verified", "wrong_id", "error", "nil"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t, tvSnapshot())
			m := metadata{tv: func(_ context.Context, id int64, _ string) (*ports.TVDetails, error) {
				d := &ports.TVDetails{TMDBID: id, Title: "Wrong", Overview: "TV plot", PosterURL: "https://image.tmdb.org/t/p/w342/tv.jpg"}
				if mode == "wrong_id" {
					d.TMDBID++
				}
				if mode == "error" {
					return d, errInjected
				}
				if mode == "nil" {
					return nil, nil
				}
				return d, nil
			}}
			a := artwork(func(_ context.Context, path string) ([]byte, error) {
				if mode != "verified" || path != "/tv.jpg" {
					t.Error("unverified TV artwork")
				}
				return []byte{1}, nil
			})
			if err := f.execute(m, a); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(f.nfo(), "TV plot") != (mode == "verified") || strings.Contains(f.nfo(), "Wrong") {
				t.Fatal("TV identity violated")
			}
		})
	}
}

func TestRootNamespaceMarkerSourceAndDirectoryFences(t *testing.T) {
	for _, mode := range []string{"pinned_root", "missing_namespace", "changed_namespace", "marker_missing", "marker_symlink", "marker_directory", "source_size", "source_inode", "source_mtime", "source_missing", "directory_device", "directory_changed", "namespace_after_nfo", "source_after_nfo"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t, movieSnapshot())
			originalInspect := f.fs.pinned.inspect
			switch mode {
			case "pinned_root":
				f.fs.pinned.id.Inode++
			case "missing_namespace":
				f.fs.openHook = func() error { return errInjected }
			case "changed_namespace":
				f.fs.namespace.id.Inode++
			case "marker_missing", "marker_symlink", "marker_directory":
				f.fs.namespace.inspect = func(path string) (ports.FileIdentity, error) {
					if path == ".pulsemedia_root" {
						return ports.FileIdentity{}, ports.ErrFileSymlink
					}
					return originalInspect(path)
				}
			case "source_size", "source_inode", "source_mtime", "source_missing":
				f.fs.pinned.inspect = func(path string) (ports.FileIdentity, error) {
					id, err := originalInspect(path)
					if mode == "source_missing" {
						return id, errInjected
					}
					if mode == "source_size" {
						id.Size++
					}
					if mode == "source_inode" {
						id.Inode++
					}
					if mode == "source_mtime" {
						id.Mtime++
					}
					return id, err
				}
			case "directory_device":
				f.fs.dir.Device++
			case "directory_changed":
				calls := 0
				f.fs.dirHook = func() (ports.SidecarDirectoryIdentity, error) {
					calls++
					id := f.fs.dir
					if calls > 1 {
						id.Inode++
					}
					return id, nil
				}
			case "namespace_after_nfo":
				f.fs.publish = func(ports.SidecarRequest) (ports.SidecarResult, error) {
					f.fs.namespace.id.Inode++
					return created(), nil
				}
			case "source_after_nfo":
				f.fs.publish = func(ports.SidecarRequest) (ports.SidecarResult, error) {
					f.fs.pinned.inspect = func(string) (ports.FileIdentity, error) { return ports.FileIdentity{}, errInjected }
					return created(), nil
				}
			}
			f.repo.snapshot.PosterPath = "/safe.jpg"
			err := f.execute(nil, artwork(func(context.Context, string) ([]byte, error) { return []byte{1}, nil }))
			if err == nil {
				t.Fatal("unsafe filesystem accepted")
			}
			wantWrites := 0
			if strings.HasSuffix(mode, "after_nfo") {
				wantWrites = 1
				if f.repo.completion != nil {
					t.Fatal("normal completion accepted after failed physical fence")
				}
			}
			if len(f.fs.requests) != wantWrites || !f.fs.released.Load() {
				t.Fatal("unsafe write or slot leak", f.events)
			}
		})
	}
}

func TestBlockedSlotRevalidatesCurrentLeaseAndOwner(t *testing.T) {
	f := setup(t, movieSnapshot())
	entered, resume := make(chan struct{}), make(chan struct{})
	var stale atomic.Bool
	f.fs.lockHook = func() { close(entered); <-resume }
	f.repo.validate = func(_ int, _ ports.MediaSidecarSnapshot) error {
		if stale.Load() {
			return ports.ErrMediaSidecarStale
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- f.execute(nil, nil) }()
	await(t, entered)
	stale.Store(true)
	close(resume)
	if err := result(t, done); !errors.Is(err, ports.ErrMediaSidecarStale) {
		t.Fatal(err)
	}
	f.noEffects()
	if !reflect.DeepEqual(f.events, []string{"load", "lock", "validate", "release"}) {
		t.Fatal("observed FS before renewed authority", f.events)
	}
}

func TestValidationBeforeEachArtifactAndAfterIO(t *testing.T) {
	for _, at := range []int{2, 3, 4, 5, 6} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			f := setup(t, movieSnapshot())
			f.repo.snapshot.PosterPath = "/safe.jpg"
			f.repo.validate = func(n int, _ ports.MediaSidecarSnapshot) error {
				if n == at {
					return ports.ErrMediaSidecarStale
				}
				return nil
			}
			err := f.execute(nil, artwork(func(context.Context, string) ([]byte, error) { return []byte{1}, nil }))
			if !errors.Is(err, ports.ErrMediaSidecarStale) || f.repo.completion != nil {
				t.Fatal("stale lease completed", err)
			}
			want := 0
			if at >= 3 {
				want = 1
			}
			if at >= 5 {
				want = 2
			}
			if len(f.fs.requests) != want {
				t.Fatal("effect after stale validation", f.events)
			}
		})
	}
}

func TestPublicationOutcomesNeverReplayOrInventOwnership(t *testing.T) {
	for _, disposition := range []ports.SidecarDisposition{ports.SidecarNotPublished, ports.SidecarUnsafeExisting, ports.SidecarUncertain, ports.SidecarPreservedRegular, ports.SidecarCreated} {
		t.Run(string(disposition), func(t *testing.T) {
			for _, withError := range []bool{false, true} {
				t.Run(fmt.Sprint(withError), func(t *testing.T) {
					f := setup(t, movieSnapshot())
					f.repo.snapshot.PosterPath = "/safe.jpg"
					outcome := created()
					outcome.Disposition = disposition
					outcome.Published = disposition == ports.SidecarCreated || disposition == ports.SidecarUncertain
					if disposition == ports.SidecarNotPublished || disposition == ports.SidecarUnsafeExisting {
						outcome.Identity = ports.FileIdentity{}
					}
					f.fs.publish = func(request ports.SidecarRequest) (ports.SidecarResult, error) {
						actual := outcome
						if request.Name == "poster.jpg" {
							actual.Identity.Inode++
						}
						if withError {
							return actual, errInjected
						}
						return actual, nil
					}
					err := f.execute(nil, artwork(func(context.Context, string) ([]byte, error) { return []byte{1}, nil }))
					success := !withError && (disposition == ports.SidecarCreated || disposition == ports.SidecarPreservedRegular)
					if (err == nil) != success {
						t.Fatal("publication error swallowed", err)
					}
					want := 1
					if success {
						want = 2
					}
					if len(f.fs.requests) != want {
						t.Fatal("replayed actual outcome", f.events)
					}
					if withError && (disposition == ports.SidecarCreated || disposition == ports.SidecarPreservedRegular) {
						if f.repo.completion != nil {
							t.Fatal("successful completion despite native error")
						}
						return
					}
					if f.repo.completion == nil || f.repo.completion.Artifacts[0].Result != outcome {
						t.Fatal("lost actual outcome", f.events)
					}
					if disposition == ports.SidecarPreservedRegular && f.repo.completion.Artifacts[0].Result.Published {
						t.Fatal("claimed existing file")
					}
				})
			}
		})
	}
}

func TestCompletionFailureKeepsPublishedFilesAndSlot(t *testing.T) {
	f := setup(t, movieSnapshot())
	f.repo.completeErr = errInjected
	f.repo.completeHook = func() {
		if f.fs.released.Load() {
			t.Error("released slot before Complete returned")
		}
	}
	if err := f.execute(nil, nil); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	if len(f.fs.requests) != 1 || f.repo.completion.Artifacts[0].Result != created() || !f.fs.released.Load() {
		t.Fatal("publication revoked or replayed")
	}
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("test barrier timed out")
	}
}
func result(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return")
		return nil
	}
}

func TestCancellationWaitsForContextIgnoringNetwork(t *testing.T) {
	for _, stage := range []string{"metadata", "artwork"} {
		t.Run(stage, func(t *testing.T) {
			f := setup(t, movieSnapshot())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.ctx = ctx
			entered, resume := make(chan struct{}), make(chan struct{})
			m := metadata{movie: func(context.Context, int64, string) (*ports.MovieMetadata, error) {
				if stage == "metadata" {
					close(entered)
					<-resume
				}
				return &ports.MovieMetadata{TMDBID: 77, MediaType: "movie", PosterURL: "https://image.tmdb.org/t/p/w342/safe.jpg"}, nil
			}}
			a := artwork(func(context.Context, string) ([]byte, error) {
				if stage == "artwork" {
					close(entered)
					<-resume
				}
				return []byte{1}, nil
			})
			done := make(chan error, 1)
			go func() { done <- f.execute(m, a) }()
			await(t, entered)
			cancel()
			select {
			case <-done:
				t.Error("Execute abandoned physical network call")
			default:
			}
			close(resume)
			if err := result(t, done); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			f.noEffects()
			if f.fs.locked {
				t.Fatal("filesystem entered after cancellation", f.events)
			}
		})
	}
}

func TestCancellationRetainsSlotUntilNativeOperationReturns(t *testing.T) {
	for _, stage := range []string{"namespace", "publish"} {
		t.Run(stage, func(t *testing.T) {
			f := setup(t, movieSnapshot())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.ctx = ctx
			entered, resume := make(chan struct{}), make(chan struct{})
			if stage == "namespace" {
				f.fs.openHook = func() error { close(entered); <-resume; return nil }
			} else {
				f.fs.publish = func(ports.SidecarRequest) (ports.SidecarResult, error) {
					close(entered)
					<-resume
					return created(), nil
				}
			}
			done := make(chan error, 1)
			go func() { done <- f.execute(nil, nil) }()
			await(t, entered)
			cancel()
			if f.fs.released.Load() {
				t.Error("released slot during actual I/O")
			}
			select {
			case <-done:
				t.Error("abandoned native call")
			default:
			}
			close(resume)
			if err := result(t, done); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if !f.fs.released.Load() || f.repo.completion != nil {
				t.Fatal("cancel fabricated success or leaked slot")
			}
			if stage == "publish" && len(f.fs.requests) != 1 {
				t.Fatal("publication replayed")
			}
		})
	}
}

func TestSnapshotAndContentIsolation(t *testing.T) {
	f := setup(t, tvSnapshot())
	originalYear, originalSeason := *f.repo.snapshot.Year, *f.repo.snapshot.SeasonNumber
	poster := []byte{1, 2, 3}
	m := metadata{tv: func(context.Context, int64, string) (*ports.TVDetails, error) {
		*f.repo.snapshot.Year, *f.repo.snapshot.SeasonNumber, *f.repo.snapshot.Mappings[0].SeasonNumber = 1900, 9, 9
		f.repo.snapshot.Mappings[0].MediaID = uuid.New()
		return &ports.TVDetails{TMDBID: 77, Overview: "Frozen plot", PosterURL: "https://image.tmdb.org/t/p/w342/safe.jpg"}, nil
	}}
	f.repo.validate = func(_ int, s ports.MediaSidecarSnapshot) error {
		if *s.Year != originalYear || *s.SeasonNumber != originalSeason || *s.Mappings[0].SeasonNumber != originalSeason {
			t.Error("snapshot aliased")
		}
		*s.Year, *s.SeasonNumber, *s.Mappings[0].SeasonNumber = -1, -1, -1
		s.Mappings[0].MediaID = uuid.Nil
		return nil
	}
	f.fs.lockHook = func() { poster[0] = 99 }
	f.fs.publish = func(request ports.SidecarRequest) (ports.SidecarResult, error) {
		if request.Name == "poster.jpg" && !bytes.Equal(request.Content, []byte{1, 2, 3}) {
			t.Error("provider bytes aliased", request.Content)
		}
		for i := range request.Content {
			request.Content[i] = 0
		}
		return created(), nil
	}
	if err := f.execute(m, artwork(func(context.Context, string) ([]byte, error) { return poster, nil })); err != nil {
		t.Fatal(err)
	}
	if *f.repo.completedSnapshot.Year != originalYear || *f.repo.completedSnapshot.SeasonNumber != originalSeason || !strings.Contains(f.nfo(), "Frozen plot") {
		t.Fatal("frozen content changed")
	}
}

func TestLoadFailureAndPreCanceledContextHaveNoEffects(t *testing.T) {
	for _, mode := range []string{"load_failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t, movieSnapshot())
			want := errInjected
			if mode == "load_failure" {
				f.repo.loadErr = errInjected
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				f.ctx = ctx
				want = context.Canceled
			}
			if err := f.execute(nil, nil); !errors.Is(err, want) {
				t.Fatal(err)
			}
			f.noEffects()
			if !reflect.DeepEqual(f.events, []string{"load"}) {
				t.Fatal("Load not first or extra I/O", f.events)
			}
		})
	}
}

func TestCustomMarkerAndCurrentPosterWithoutMetadata(t *testing.T) {
	f := setup(t, movieSnapshot())
	f.repo.snapshot.MarkerFilename = "custom.marker"
	f.repo.snapshot.PosterPath = "/current.jpg"
	inspect := f.fs.namespace.inspect
	f.fs.namespace.inspect = func(path string) (ports.FileIdentity, error) {
		if path != "custom.marker" {
			t.Error("configured marker ignored", path)
		}
		return inspect(path)
	}
	if err := f.execute(nil, artwork(func(_ context.Context, path string) ([]byte, error) {
		if path != "/current.jpg" {
			t.Error("current opaque poster ignored", path)
		}
		return []byte{1, 2, 3}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if len(f.repo.completion.Artifacts) != 2 {
		t.Fatal("missing trusted current artwork")
	}
}

func TestCancellationRetainsSlotThroughCompletion(t *testing.T) {
	f := setup(t, movieSnapshot())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.ctx = ctx
	entered, resume := make(chan struct{}), make(chan struct{})
	f.repo.completeHook = func() { close(entered); <-resume }
	done := make(chan error, 1)
	go func() { done <- f.execute(nil, nil) }()
	await(t, entered)
	cancel()
	if f.fs.released.Load() {
		t.Error("slot released during actual Complete")
	}
	select {
	case <-done:
		t.Error("Complete abandoned before actual return")
	default:
	}
	close(resume)
	if err := result(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !f.fs.released.Load() || len(f.fs.requests) != 1 {
		t.Fatal("slot leak or replay")
	}
}
