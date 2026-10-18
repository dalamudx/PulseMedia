package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type movieUpgradeNativeDriver struct {
	manager  *Manager
	verifier ports.MovieUpgradeNativeDispatchVerifier
	clock    ports.Clock
}

func NewMovieUpgradeNativeSendDriver(manager *Manager, verifier ports.MovieUpgradeNativeDispatchVerifier, clock ports.Clock) (ports.MovieUpgradeNativeSendDriver, error) {
	if manager == nil || verifier == nil || clock == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradeNativeDriver{manager, verifier, clock}, nil
}

func (d *movieUpgradeNativeDriver) OpenMovieUpgradeNativeSend(ctx context.Context, expected ports.MovieUpgradeNativeDescriptor) (ports.MovieUpgradeNativeSendAttempt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if expected.OperationID == uuid.Nil || expected.TaskID == uuid.Nil || expected.ClientID == uuid.Nil || len(expected.ClientDigest) != 64 || expected.SavePath == "" || len(expected.InfoHash) != 40 || len(expected.RawSHA256) != 64 {
		return nil, ports.ErrMovieUpgradeMetainfoReview
	}
	release, err := d.manager.acquireClientBudget(ctx, expected.ClientID)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	row, err := d.manager.queries.GetDownloadClientByID(ctx, expected.ClientID)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ports.ErrUnavailable
	}
	if inventoryClientStamp(row) != expected.ClientDigest {
		return nil, ports.ErrUpgradeReservationConflict
	}
	d.manager.mu.RLock()
	factory := d.manager.factory
	d.manager.mu.RUnlock()
	if factory == nil {
		return nil, ports.ErrUnavailable
	}
	password, err := crypto.Decrypt(d.manager.masterKey, row.PasswordEncrypted)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	client, err := factory(row.ClientType, row.Url, row.Username, string(password))
	if err != nil || client == nil {
		return nil, ports.ErrUnavailable
	}
	capable, ok := client.(interface{ MovieUpgradeByteSender() ports.DownloadClient })
	if !ok {
		return nil, ports.ErrMovieUpgradeMetainfoReview
	}
	native := capable.MovieUpgradeByteSender()
	if native == nil {
		return nil, ports.ErrUnavailable
	}
	keep = true
	return &movieUpgradeNativeAttempt{driver: d, descriptor: expected, client: native, release: release}, nil
}

type movieUpgradeNativeAttempt struct {
	driver                     *movieUpgradeNativeDriver
	descriptor                 ports.MovieUpgradeNativeDescriptor
	client                     ports.DownloadClient
	release                    func()
	mu                         sync.Mutex
	attempted, running, closed bool
	returned                   *movieUpgradeNativeReturn
}
type movieUpgradeNativeReturn struct {
	issuer     *movieUpgradeNativeAttempt
	dispatchID uuid.UUID
	binding    ports.MovieUpgradeSendReturnBinding
	status     string
	returnedAt time.Time
}

func (a *movieUpgradeNativeAttempt) ExecuteMovieUpgradeSend(ctx context.Context, claim ports.MovieUpgradeNativeDispatchProof) (ports.MovieUpgradeSendReturnProof, error) {
	a.mu.Lock()
	if a.attempted || a.closed {
		a.mu.Unlock()
		return nil, ports.ErrMovieUpgradeSendObservation
	}
	a.attempted, a.running = true, true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.running = false; a.mu.Unlock() }()
	payload, err := a.driver.verifier.ConsumeMovieUpgradeDispatch(ctx, claim, a.descriptor)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload.TorrentBytes)
	if payload.DispatchID == uuid.Nil || payload.Binding.OperationID != a.descriptor.OperationID || payload.Binding.TaskID != a.descriptor.TaskID || payload.Binding.RawSHA256 != a.descriptor.RawSHA256 || hex.EncodeToString(sum[:]) != a.descriptor.RawSHA256 {
		return nil, ports.ErrMovieUpgradeMetainfoReview
	}
	addErr := a.client.AddTorrent(ctx, ports.AddTorrentParam{TorrentBytes: payload.TorrentBytes, InfoHash: a.descriptor.InfoHash, SavePath: a.descriptor.SavePath, Tags: "pulsemedia,pulsemedia:op:" + a.descriptor.OperationID.String()})
	status := "confirmed"
	if addErr != nil {
		status = "unknown"
	}
	p := &movieUpgradeNativeReturn{issuer: a, dispatchID: payload.DispatchID, binding: payload.Binding, status: status, returnedAt: a.driver.clock.Now().UTC().Truncate(time.Microsecond)}
	a.mu.Lock()
	a.returned = p
	a.mu.Unlock()
	return p, nil
}

func (a *movieUpgradeNativeAttempt) VerifyMovieUpgradeSendReturn(ctx context.Context, claim ports.MovieUpgradeSendReturnProof, expected ports.MovieUpgradeSendReturnBinding) (ports.MovieUpgradeSendVerifiedReturn, error) {
	var zero ports.MovieUpgradeSendVerifiedReturn
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	p, ok := claim.(*movieUpgradeNativeReturn)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !ok || p == nil || a.running || a.closed || a.returned != p || p.issuer != a || p.dispatchID == uuid.Nil || p.binding != expected {
		return zero, ports.ErrUpgradeReservationConflict
	}
	return ports.MovieUpgradeSendVerifiedReturn{Binding: p.binding, Status: p.status, ReturnedAt: p.returnedAt}, nil
}

func (a *movieUpgradeNativeAttempt) Close(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return ports.ErrMovieUpgradeSendObservation
	}
	if !a.closed {
		a.closed = true
		a.release()
	}
	return nil
}
