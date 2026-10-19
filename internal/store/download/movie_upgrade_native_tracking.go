package download

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type movieUpgradeTrackingDriver struct {
	manager   *Manager
	verifier  ports.MovieUpgradeTrackingQueryVerifier
	inspector ports.MovieUpgradeTrackingMetainfoInspector
	clock     ports.Clock
}

func NewMovieUpgradeNativeTrackingDriver(m *Manager, v ports.MovieUpgradeTrackingQueryVerifier, i ports.MovieUpgradeTrackingMetainfoInspector, c ports.Clock) (ports.MovieUpgradeNativeTrackingDriver, error) {
	if m == nil || v == nil || i == nil || c == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradeTrackingDriver{m, v, i, c}, nil
}
func cloneMovieTrackingDescriptor(d ports.MovieUpgradeTrackingDescriptor) ports.MovieUpgradeTrackingDescriptor {
	d.Target.Identities = slices.Clone(d.Target.Identities)
	d.Target.Aliases = slices.Clone(d.Target.Aliases)
	d.Files = slices.Clone(d.Files)
	return d
}
func validMovieTrackingDescriptor(d ports.MovieUpgradeTrackingDescriptor) bool {
	i := d.Identity
	if i.OperationID == uuid.Nil || i.TaskID == uuid.Nil || i.ClientID == uuid.Nil || i.OperationTag != "pulsemedia:op:"+i.OperationID.String() || i.SavePath == "" || strings.ContainsAny(i.SavePath, "\x00\r\n") || i.TotalBytes <= 0 {
		return false
	}
	for s, size := range map[string]int{i.InfoHash: 40, i.ClientDigest: 64} {
		if len(s) != size || strings.ToLower(s) != s {
			return false
		}
		if _, err := hex.DecodeString(s); err != nil {
			return false
		}
	}
	if _, err := media.ValidateMovieUpgradeTorrentFiles(d.Files, d.Target); err != nil {
		return false
	}
	var total int64
	for _, f := range d.Files {
		if f.Size < 0 || f.Size > math.MaxInt64-total {
			return false
		}
		total += f.Size
	}
	return total == i.TotalBytes
}
func (d *movieUpgradeTrackingDriver) OpenMovieUpgradeNativeTracking(ctx context.Context, expected ports.MovieUpgradeTrackingDescriptor) (ports.MovieUpgradeTrackingAttempt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expected = cloneMovieTrackingDescriptor(expected)
	if !validMovieTrackingDescriptor(expected) {
		return nil, ports.ErrMovieUpgradeTrackingReview
	}
	release, err := d.manager.acquireClientBudget(ctx, expected.Identity.ClientID)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	config, err := d.manager.queries.GetDownloadClientByID(ctx, expected.Identity.ClientID)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ports.ErrUnavailable
	}
	if inventoryClientStamp(config) != expected.Identity.ClientDigest {
		return nil, ports.ErrUpgradeReservationConflict
	}
	d.manager.mu.RLock()
	factory := d.manager.factory
	d.manager.mu.RUnlock()
	if factory == nil {
		return nil, ports.ErrUnavailable
	}
	password, err := crypto.Decrypt(d.manager.masterKey, config.PasswordEncrypted)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	client, err := factory(config.ClientType, config.Url, config.Username, string(password))
	if err != nil || client == nil {
		return nil, ports.ErrUnavailable
	}
	capable, ok := client.(interface {
		CompleteTVTrackingObserver() ports.CompleteTVTrackingClient
	})
	if !ok {
		return nil, ports.ErrMovieUpgradeTrackingReview
	}
	native := capable.CompleteTVTrackingObserver()
	if native == nil {
		return nil, ports.ErrUnavailable
	}
	keep = true
	return &movieUpgradeTrackingAttempt{driver: d, descriptor: expected, client: native, release: release}, nil
}

type movieUpgradeTrackingAttempt struct {
	driver                     *movieUpgradeTrackingDriver
	descriptor                 ports.MovieUpgradeTrackingDescriptor
	client                     ports.CompleteTVTrackingClient
	release                    func()
	mu                         sync.Mutex
	attempted, running, closed bool
	returned                   *movieUpgradeTrackingReturn
}
type movieUpgradeTrackingReturn struct {
	issuer *movieUpgradeTrackingAttempt
	value  ports.MovieUpgradeVerifiedTracking
}

func (a *movieUpgradeTrackingAttempt) QueryMovieUpgradeTracking(ctx context.Context, proof ports.MovieUpgradeTrackingQueryProof) (ports.MovieUpgradeTrackingProof, error) {
	a.mu.Lock()
	if a.closed || a.attempted {
		a.mu.Unlock()
		return nil, ports.ErrMovieUpgradeTrackingReview
	}
	a.attempted, a.running = true, true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.running = false; a.mu.Unlock() }()
	binding, err := a.driver.verifier.ConsumeMovieUpgradeTrackingQuery(ctx, proof, cloneMovieTrackingDescriptor(a.descriptor))
	if err != nil {
		return nil, err
	}
	i := a.descriptor.Identity
	if binding.OperationID != i.OperationID || binding.TaskID != i.TaskID || binding.ObservationID == uuid.Nil || binding.Owner == uuid.Nil || binding.Token == uuid.Nil || !movieTrackingSHA(binding.ObservationSHA256) || !movieTrackingSHA(binding.PermitSHA256) {
		return nil, ports.ErrUpgradeReservationConflict
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	value := ports.MovieUpgradeVerifiedTracking{Binding: binding}
	outcome, reason, evidence := a.observe(ctx, &value)
	value.Outcome, value.Reason, value.Evidence = outcome, reason, evidence
	value.ReturnedAt = a.driver.clock.Now().UTC().Truncate(time.Microsecond)
	p := &movieUpgradeTrackingReturn{issuer: a, value: value}
	a.mu.Lock()
	a.returned = p
	a.mu.Unlock()
	return p, nil
}
func movieTrackingSHA(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil
}
func movieTrackingIdentity(rows []*ports.TorrentInfo, i ports.MovieUpgradeNativeObservationDescriptor) (*ports.TorrentInfo, bool) {
	if len(rows) != 1 || rows[0] == nil {
		return nil, false
	}
	r := *rows[0]
	if r.InfoHash != i.InfoHash || r.SavePath != i.SavePath || r.TotalBytes != i.TotalBytes || strings.ContainsAny(r.Tags, "\x00\r\n") {
		return nil, false
	}
	n := 0
	for _, tag := range strings.Split(r.Tags, ",") {
		if strings.TrimSpace(tag) == i.OperationTag {
			n++
		}
	}
	p := float64(r.Progress)
	if n != 1 || math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 || r.DownloadedBytes < 0 || r.DownloadedBytes > i.TotalBytes {
		return nil, false
	}
	switch r.State {
	case "uploading", "stalledUP", "queuedUP", "forcedUP", "pausedUP", "stoppedUP", "downloading", "stalledDL", "queuedDL", "forcedDL", "pausedDL", "stoppedDL", "checkingDL", "checkingUP", "metaDL", "forcedMetaDL", "allocating", "moving", "checkingResumeData":
	default:
		return nil, false
	}
	return &r, true
}
func movieTrackingComplete(r *ports.TorrentInfo) bool {
	if r == nil || r.Progress != 1 || r.DownloadedBytes != r.TotalBytes {
		return false
	}
	switch r.State {
	case "uploading", "stalledUP", "queuedUP", "forcedUP", "pausedUP", "stoppedUP":
		return true
	default:
		return false
	}
}
func movieTrackingInventory(files []ports.TorrentFile, expected []media.TVTorrentFile) bool {
	if len(files) == 0 || len(files) != len(expected) {
		return false
	}
	names := make(map[string]int64, len(expected))
	for _, f := range expected {
		if _, ok := names[f.Path]; ok {
			return false
		}
		names[f.Path] = f.Size
	}
	indexes := make(map[int32]bool, len(files))
	for _, f := range files {
		size, ok := names[f.Name]
		if !ok || f.SizeBytes != size || f.Progress != 1 || f.Index < 0 || int64(f.Index) >= int64(len(files)) || indexes[f.Index] {
			return false
		}
		indexes[f.Index] = true
		delete(names, f.Name)
	}
	return len(names) == 0
}
func (a *movieUpgradeTrackingAttempt) observe(ctx context.Context, telemetry *ports.MovieUpgradeVerifiedTracking) (string, string, json.RawMessage) {
	manual := func(reason string) (string, string, json.RawMessage) {
		return "manual_review", reason, json.RawMessage(`{"version":"movie-upgrade.tracking.v1"}`)
	}
	i := a.descriptor.Identity
	rows, err := a.client.ListTorrents(ctx, i.OperationTag)
	if err != nil {
		return manual("query_failed")
	}
	before, ok := movieTrackingIdentity(rows, i)
	if !ok {
		return manual("identity_or_progress_mismatch")
	}
	complete := movieTrackingComplete(before)
	var files []ports.TorrentFile
	if complete {
		raw, e := a.client.GetTorrentMetainfo(ctx, i.InfoHash)
		if e != nil {
			return manual("metainfo_unavailable")
		}
		proof, e := a.driver.inspector.InspectMovieUpgradeTrackingMetainfo(ctx, raw, a.descriptor.Target)
		if e != nil || proof == nil {
			return manual("metainfo_mismatch")
		}
		if e = a.driver.inspector.VerifyMovieUpgradeMetainfo(ctx, proof, a.descriptor.Target); e != nil {
			return manual("metainfo_mismatch")
		}
		if proof.InfoHash() != i.InfoHash || proof.TotalBytes() != i.TotalBytes || !slices.Equal(proof.Files(), a.descriptor.Files) {
			return manual("metainfo_mismatch")
		}
		files, e = a.client.GetTorrentFiles(ctx, i.InfoHash)
		if e != nil {
			return manual("inventory_unavailable")
		}
		if !movieTrackingInventory(files, a.descriptor.Files) {
			return manual("inventory_mismatch")
		}
	}
	rows, err = a.client.ListTorrents(ctx, i.OperationTag)
	if err != nil {
		return manual("query_failed")
	}
	after, ok := movieTrackingIdentity(rows, i)
	if !ok {
		return manual("identity_or_progress_mismatch")
	}
	if after.Progress < before.Progress || after.DownloadedBytes < before.DownloadedBytes {
		return manual("progress_regressed")
	}
	if !complete {
		if telemetry != nil {
			progress := float64(after.Progress)
			telemetry.NativeProgress = &progress
			if after.DownloadRateBytesPerSecond != nil {
				rate := *after.DownloadRateBytesPerSecond
				telemetry.DownloadRateBytesPerSecond = &rate
			}
		}
		return "tracking", "download_in_progress", json.RawMessage(`{"version":"movie-upgrade.tracking.v1"}`)
	}
	if !movieTrackingComplete(after) {
		return manual("completion_changed")
	}
	evidence, err := json.Marshal(struct {
		Version  string                `json:"version"`
		InfoHash string                `json:"info_hash"`
		Total    int64                 `json:"total_bytes"`
		Files    []media.TVTorrentFile `json:"files"`
	}{"movie-upgrade.tracking.v1", i.InfoHash, i.TotalBytes, slices.Clone(a.descriptor.Files)})
	if err != nil {
		return manual("inventory_mismatch")
	}
	return "completed", "organization_review_required", evidence
}
func (a *movieUpgradeTrackingAttempt) VerifyMovieUpgradeTracking(ctx context.Context, proof ports.MovieUpgradeTrackingProof, binding ports.MovieUpgradeObservationBinding) (ports.MovieUpgradeVerifiedTracking, error) {
	var zero ports.MovieUpgradeVerifiedTracking
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	p, ok := proof.(*movieUpgradeTrackingReturn)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !ok || p == nil || a.closed || a.running || a.returned != p || p.issuer != a || p.value.Binding != binding {
		return zero, ports.ErrUpgradeReservationConflict
	}
	out := p.value
	out.Evidence = slices.Clone(out.Evidence)
	if out.NativeProgress != nil {
		value := *out.NativeProgress
		out.NativeProgress = &value
	}
	if out.DownloadRateBytesPerSecond != nil {
		value := *out.DownloadRateBytesPerSecond
		out.DownloadRateBytesPerSecond = &value
	}
	return out, nil
}
func (a *movieUpgradeTrackingAttempt) Close(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return ports.ErrMovieUpgradeTrackingReview
	}
	if !a.closed {
		a.closed = true
		a.release()
	}
	return nil
}
