package app

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type downloadWatchOptions struct {
	refresh, timeout                                      time.Duration
	observationTimeout                                    time.Duration
	watchers, hints, targets, pageSize, pages, identities int
	report                                                func(string)
}

func (a *App) runDownloadWatchWorker(ctx context.Context, repo ports.DownloadWatchRepository, observer ports.DownloadWatchObserver, files ports.RootChangeFilesystem, organizationWake chan<- struct{}) {
	a.runDownloadWatch(ctx, repo, observer, files, organizationWake, downloadWatchOptions{})
}

type downloadWatchSourceRun struct {
	binding    ports.DownloadWatchBinding
	cancel     context.CancelFunc
	done       chan struct{}
	metadataMu sync.RWMutex
	manifestAt time.Time
	instanceID uuid.UUID
	sequence   int64
}
type downloadWatchHint struct {
	source *downloadWatchSourceRun
	hint   ports.RootChangeHint
}
type downloadWatchObservation struct {
	source *downloadWatchSourceRun
	target ports.DownloadWatchTarget
}
type downloadWatchPin struct {
	binding  ports.DownloadWatchBinding
	identity ports.FileIdentity
}

// Every callback runs in an owned worker. Cancellation never substitutes for joining.
func (a *App) runDownloadWatch(ctx context.Context, repo ports.DownloadWatchRepository, observer ports.DownloadWatchObserver, files ports.RootChangeFilesystem, wake chan<- struct{}, o downloadWatchOptions) {
	if o.refresh <= 0 {
		o.refresh = 3 * time.Second
	}
	if o.timeout <= 0 {
		o.timeout = 20 * time.Second
	}
	if o.observationTimeout <= 0 {
		o.observationTimeout = o.timeout
	}
	if o.watchers <= 0 {
		o.watchers = 64
	}
	if o.hints <= 0 {
		o.hints = 128
	}
	if o.targets <= 0 {
		o.targets = 64
	}
	if o.pageSize <= 0 {
		o.pageSize = 32
	}
	if o.pages <= 0 {
		o.pages = 8
	}
	if o.identities <= 0 {
		o.identities = 4096
	}
	report := func(reason string) {
		if o.report != nil {
			o.report(reason)
		}
		if a != nil && a.logger != nil {
			a.logger.Warn("download watch coverage incomplete", slog.String("reason", reason))
		}
	}
	hints := make(chan downloadWatchHint, o.hints)
	targets := make(chan downloadWatchObservation, o.targets)
	var workers sync.WaitGroup
	var sourceContexts sync.Map
	closedContext, closeContext := context.WithCancel(ctx)
	closeContext()
	sourceContext := func(s *downloadWatchSourceRun) context.Context {
		if value, ok := sourceContexts.Load(s); ok {
			return value.(context.Context)
		}
		return closedContext
	}
	var pending sync.Map // Bounded by the target queue plus the single active observation.
	type hintResume struct {
		stamp  string
		hint   ports.RootChangeHint
		cursor string
	}
	var resumeMu sync.Mutex
	resumes := make(map[string]hintResume)
	workers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case work := <-targets:
				func() {
					defer pending.Delete(downloadWatchTargetIdentity(work.target))
					if sourceContext(work.source).Err() != nil {
						return
					}
					callCtx, cancel := context.WithTimeout(sourceContext(work.source), o.observationTimeout)
					defer cancel()
					if a != nil && work.target.Kind == "ordinary" {
						finish := a.stages.begin(ports.StageDownloadObserve)
						defer finish()
					}
					result, err := observer.ObserveDownloadWatchTarget(callCtx, work.target)
					if errors.Is(err, ports.ErrDownloadWatchStale) {
						work.source.cancel()
						report("observer_binding_stale")
					}
					if err == nil && result != nil && result.OrganizationState == "queued" {
						select {
						case wake <- struct{}{}:
						default:
						}
					}
				}()
			}
		}
	})
	workers.Go(func() {
		// A capped rescan resumes at its keyset cursor on the next controlled rebuild.
		for {
			select {
			case <-ctx.Done():
				return
			case work := <-hints:
				s := work.source
				if sourceContext(s).Err() != nil {
					continue
				}
				key := s.binding.Stamp + "\x00" + string(work.hint.Kind) + "\x00" + work.hint.RelativePath
				if work.hint.Metadata.EventID != uuid.Nil {
					key += "\x00" + work.hint.Metadata.EventID.String()
				}
				resumeMu.Lock()
				cursor := resumes[key].cursor
				resumeMu.Unlock()
				for n := 0; n < o.pages && sourceContext(s).Err() == nil; n++ {
					callCtx, cancel := context.WithTimeout(sourceContext(s), o.timeout)
					page, err := repo.ResolveDownloadWatchTargets(callCtx, s.binding, work.hint, ports.DownloadWatchTargetQuery{Cursor: cursor, Limit: o.pageSize})
					expired := callCtx.Err() != nil
					cancel()
					if err != nil || expired {
						s.cancel()
						report("target_refresh_failed")
						break
					}
					if len(page.Targets) > o.pageSize {
						s.cancel()
						report("target_page_invalid")
						break
					}
					for _, target := range page.Targets {
						if sourceContext(s).Err() != nil {
							break
						}
						targetID := downloadWatchTargetIdentity(target)
						if target.Binding != s.binding || targetID == uuid.Nil {
							s.cancel()
							report("target_binding_invalid")
							break
						}
						if _, loaded := pending.LoadOrStore(targetID, struct{}{}); loaded {
							continue
						}
						select {
						case targets <- downloadWatchObservation{s, target}:
						case <-sourceContext(s).Done():
							pending.Delete(targetID)
						}
					}
					if sourceContext(s).Err() != nil {
						break
					}
					if page.NextCursor == "" {
						resumeMu.Lock()
						delete(resumes, key)
						resumeMu.Unlock()
						if !page.Complete {
							s.cancel()
							report("target_coverage_incomplete")
						}
						break
					}
					if page.NextCursor == cursor {
						s.cancel()
						report("target_cursor_invalid")
						break
					}
					cursor = page.NextCursor
					resumeMu.Lock()
					_, known := resumes[key]
					limited := !known && len(resumes) >= o.hints
					if !limited {
						resumes[key] = hintResume{s.binding.Stamp, work.hint, cursor}
					}
					resumeMu.Unlock()
					if limited {
						s.cancel()
						report("target_resume_limit")
						break
					}
					if n+1 == o.pages {
						report("target_limit")
					}
				}
			}
		}
	})
	active := make(map[string]*downloadWatchSourceRun)
	pins := make(map[string]downloadWatchPin)
	stop := func(s *downloadWatchSourceRun) { s.cancel(); <-s.done }
	defer func() {
		for _, s := range active {
			s.cancel()
		}
		for _, s := range active {
			<-s.done
		}
		workers.Wait()
	}()
	timer := time.NewTicker(o.refresh)
	defer timer.Stop()
	cursor := ""
	for ctx.Err() == nil {
		callCtx, cancel := context.WithTimeout(ctx, o.timeout)
		page, err := repo.ListDownloadWatchSources(callCtx, ports.DownloadWatchSourceQuery{Cursor: cursor, Limit: o.watchers})
		expired := callCtx.Err() != nil
		cancel()
		selected := make(map[string]ports.DownloadWatchBinding)
		manifests := make(map[string]time.Time)
		if err != nil || expired {
			report("source_refresh_failed")
		} else if len(page.Sources) > o.watchers {
			report("source_page_invalid")
		} else {
			for _, source := range page.Sources {
				b := source.Binding
				if !validDownloadWatchBinding(b) {
					report("source_binding_invalid")
					continue
				}
				if _, exists := selected[b.Stamp]; exists {
					report("source_duplicate")
					continue
				}
				selected[b.Stamp] = b
				manifests[b.Stamp] = source.ManifestAt
			}
			if len(page.Issues) != 0 || !page.Complete || page.NextCursor != "" {
				report("source_coverage_incomplete")
			}
		}
		for stamp, s := range active {
			b, keep := selected[stamp]
			if !keep || b != s.binding || sourceContext(s).Err() != nil {
				stop(s)
				delete(active, stamp)
			}
		}
		for stamp, b := range selected {
			if ctx.Err() != nil {
				break
			}
			if s, exists := active[stamp]; exists {
				root, identity, reason := probeDownloadWatchRoot(sourceContext(s), files, b, o.timeout)
				if reason != "" {
					s.cancel()
					report(reason)
					continue
				}
				root.Close()
				if !sameDownloadWatchIdentity(identity, pins[stamp].identity) {
					s.cancel()
					report("source_identity_changed")
					continue
				}
				s.metadataMu.Lock()
				s.manifestAt = manifests[stamp]
				s.metadataMu.Unlock()
				resumeMu.Lock()
				var continuation []ports.RootChangeHint
				for _, pending := range resumes {
					if pending.stamp == stamp {
						continuation = append(continuation, pending.hint)
					}
				}
				resumeMu.Unlock()
				for _, hint := range continuation {
					select {
					case hints <- downloadWatchHint{s, hint}:
					default:
						s.cancel()
						report("hint_queue_full")
					}
				}
				continue
			}
			pin, exists := pins[stamp]
			if exists && pin.binding != b {
				report("source_stamp_conflict")
				continue
			}
			if !exists && len(pins) >= o.identities {
				report("source_identity_limit")
				continue
			}
			root, identity, reason := probeDownloadWatchRoot(ctx, files, b, o.timeout)
			if reason != "" {
				report(reason)
				continue
			}
			if exists && !sameDownloadWatchIdentity(identity, pin.identity) {
				root.Close()
				report("source_identity_changed")
				continue
			}
			pins[stamp] = downloadWatchPin{b, identity}
			sctx, scancel := context.WithCancel(ctx)
			s := &downloadWatchSourceRun{binding: b, cancel: scancel, done: make(chan struct{})}
			s.manifestAt = manifests[stamp]
			if a != nil && a.idgen != nil {
				s.instanceID = a.idgen.NewUUID()
			}
			sourceContexts.Store(s, sctx)
			active[stamp] = s
			go func(sourceCtx context.Context, source *downloadWatchSourceRun, capability ports.TransferRoot, currentIdentity ports.FileIdentity) {
				defer close(source.done)
				defer sourceContexts.Delete(source)
				a.runDownloadWatchSource(sourceCtx, source, files, capability, currentIdentity, hints, o.hints, report)
			}(sctx, s, root, identity)
		}
		if err == nil && !expired && page.NextCursor != "" && page.NextCursor == cursor {
			report("source_cursor_invalid")
			cursor = ""
		} else {
			cursor = page.NextCursor
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

func (a *App) runDownloadWatchSource(ctx context.Context, s *downloadWatchSourceRun, files ports.RootChangeFilesystem, root ports.TransferRoot, identity ports.FileIdentity, shared chan<- downloadWatchHint, size int, report func(string)) {
	defer root.Close()
	defer s.cancel()
	native := make(chan ports.RootChangeHint, size)
	var forward sync.WaitGroup
	forward.Go(func() {
		for hint := range native {
			if ctx.Err() != nil {
				continue
			}
			if !validDownloadWatchHint(hint) {
				s.cancel()
				report("hint_invalid")
				continue
			}
			var decorateErr error
			hint, decorateErr = a.decorateDownloadWatchHint(s, identity, hint)
			if decorateErr != nil {
				s.cancel()
				report("hint_identity_unavailable")
				continue
			}
			select {
			case shared <- downloadWatchHint{s, hint}:
			default:
				s.cancel()
				report("hint_queue_full")
			}
		}
	})
	err := files.WatchRoot(ctx, ports.RootWatchRequest{Root: root, ConfiguredPath: s.binding.LocalRoot, FrozenIdentity: identity}, native)
	s.cancel() // Invalidate queued hints and active observations before joining forwarding.
	close(native)
	forward.Wait()
	if err != nil {
		report("native_coverage_lost")
	}
}

func probeDownloadWatchRoot(ctx context.Context, files ports.RootChangeFilesystem, b ports.DownloadWatchBinding, timeout time.Duration) (ports.TransferRoot, ports.FileIdentity, string) {
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	library, err := files.OpenRoot(callCtx, b.RootPath)
	if err != nil {
		return nil, ports.FileIdentity{}, "library_open_failed"
	}
	defer library.Close()
	identity, err := library.Identity(callCtx)
	if err != nil || !sameDownloadWatchIdentity(identity, b.RootIdentity) {
		return nil, ports.FileIdentity{}, "library_identity_changed"
	}
	marker := b.MarkerFilename
	if marker == "" {
		marker = ".pulsemedia_root"
	}
	if _, err = library.Inspect(callCtx, marker); err != nil {
		return nil, ports.FileIdentity{}, "library_marker_missing"
	}
	source, err := files.OpenRoot(callCtx, b.LocalRoot)
	if err != nil {
		return nil, ports.FileIdentity{}, "source_open_failed"
	}
	identity, err = source.Identity(callCtx)
	if err != nil || callCtx.Err() != nil || identity.Device <= 0 || identity.Inode <= 0 || (b.SourceKind == "library_identity" && !sameDownloadWatchIdentity(identity, b.RootIdentity)) || (b.ScopeID != uuid.Nil && !sameDownloadWatchIdentity(identity, b.SourceIdentity)) {
		source.Close()
		return nil, ports.FileIdentity{}, "source_identity_invalid"
	}
	return source, identity, ""
}

func sameDownloadWatchIdentity(a, b ports.FileIdentity) bool {
	return a.Device == b.Device && a.Inode == b.Inode
}
func downloadWatchTargetIdentity(target ports.DownloadWatchTarget) uuid.UUID {
	if target.Kind == ports.UntrackedDiscoveryJobKind {
		if target.ScopeID != uuid.Nil && target.ScopeVersion > 0 && target.ScopeID == target.Binding.ScopeID && target.ScopeVersion == target.Binding.ScopeVersion && target.TaskID == uuid.Nil && target.OperationID == uuid.Nil {
			return target.ScopeID
		}
		return uuid.Nil
	}
	if target.ScopeID != uuid.Nil || target.ScopeVersion != 0 || target.Binding.ScopeID != uuid.Nil || target.TaskID == uuid.Nil {
		return uuid.Nil
	}
	return target.OperationID
}

func validDownloadWatchBinding(b ports.DownloadWatchBinding) bool {
	opaque := func(s string) bool { return s != "" && len(s) <= 512 && !strings.ContainsAny(s, "\x00\r\n\t") }
	canonical := func(s string) bool {
		return filepath.IsAbs(s) && s != "/" && filepath.Clean(s) == s && !strings.ContainsAny(s, "\x00\r\n")
	}
	if b.ClientID == uuid.Nil || b.RootID == uuid.Nil || b.BaselineScanID == uuid.Nil || b.Generation <= 0 || b.ClientVersion.IsZero() || !opaque(b.Stamp) || !opaque(b.ClientStamp) || !canonical(b.LocalRoot) || !canonical(b.RootPath) || b.RootIdentity.Device <= 0 || b.RootIdentity.Inode <= 0 {
		return false
	}
	if b.ScopeID == uuid.Nil {
		if b.ScopeVersion != 0 || b.SourceIdentity != (ports.FileIdentity{}) {
			return false
		}
	} else if b.ScopeVersion < 1 || b.SourceKind != "path_mapping" || b.SourceIdentity.Device <= 0 || b.SourceIdentity.Inode <= 0 {
		return false
	}
	if b.SourceKind != "library_identity" && b.SourceKind != "path_mapping" {
		return false
	}
	if b.SourceKind == "library_identity" && b.LocalRoot != b.RootPath {
		return false
	}
	if b.SourceKind == "library_identity" && b.RemotePrefix != "" {
		return false
	}
	if b.SourceKind == "path_mapping" && (b.RemotePrefix == "" || !path.IsAbs(b.RemotePrefix) || path.Clean(b.RemotePrefix) != b.RemotePrefix || strings.ContainsAny(b.RemotePrefix, "\x00\r\n\\")) {
		return false
	}
	return b.MarkerFilename == "" || (b.MarkerFilename != "." && b.MarkerFilename != ".." && filepath.Base(b.MarkerFilename) == b.MarkerFilename && !strings.ContainsAny(b.MarkerFilename, "\x00\r\n\\"))
}
func validDownloadWatchHint(h ports.RootChangeHint) bool {
	if h.Kind != ports.RootChangeCloseWrite && h.Kind != ports.RootChangeMovedTo && h.Kind != ports.RootChangeRescan {
		return false
	}
	return h.RelativePath != "" && !path.IsAbs(h.RelativePath) && path.Clean(h.RelativePath) == h.RelativePath && h.RelativePath != ".." && !strings.HasPrefix(h.RelativePath, "../") && !strings.ContainsAny(h.RelativePath, "\x00\\")
}
