package download

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"strings"
	"sync"
	"time"
)

type movieUpgradeObservationDriver struct {
	manager  *Manager
	verifier ports.MovieUpgradeNativeQueryVerifier
	clock    ports.Clock
}

func NewMovieUpgradeNativeObservationDriver(m *Manager, v ports.MovieUpgradeNativeQueryVerifier, clock ports.Clock) (ports.MovieUpgradeNativeObservationDriver, error) {
	if m == nil || v == nil || clock == nil {
		return nil, ports.ErrUnavailable
	}
	return &movieUpgradeObservationDriver{m, v, clock}, nil
}
func (d *movieUpgradeObservationDriver) OpenMovieUpgradeNativeObservation(ctx context.Context, expected ports.MovieUpgradeNativeObservationDescriptor) (ports.MovieUpgradeNativeObservationAttempt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if expected.OperationID == uuid.Nil || expected.TaskID == uuid.Nil || expected.ClientID == uuid.Nil || len(expected.ClientDigest) != 64 || len(expected.InfoHash) != 40 || expected.SavePath == "" || expected.TotalBytes <= 0 || expected.OperationTag != "pulsemedia:op:"+expected.OperationID.String() {
		return nil, ports.ErrMovieUpgradeObservationReview
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
	config, err := d.manager.queries.GetDownloadClientByID(ctx, expected.ClientID)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ports.ErrUnavailable
	}
	if inventoryClientStamp(config) != expected.ClientDigest {
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
		MovieUpgradeObserver() ports.MovieUpgradeTorrentObserver
	})
	if !ok {
		return nil, ports.ErrMovieUpgradeObservationReview
	}
	native := capable.MovieUpgradeObserver()
	if native == nil {
		return nil, ports.ErrUnavailable
	}
	keep = true
	return &movieUpgradeObservationAttempt{driver: d, descriptor: expected, client: native, release: release}, nil
}

type movieUpgradeObservationAttempt struct {
	driver                     *movieUpgradeObservationDriver
	descriptor                 ports.MovieUpgradeNativeObservationDescriptor
	client                     ports.MovieUpgradeTorrentObserver
	release                    func()
	mu                         sync.Mutex
	attempted, running, closed bool
	returned                   *movieUpgradeObservationReturn
}
type movieUpgradeObservationReturn struct {
	issuer     *movieUpgradeObservationAttempt
	binding    ports.MovieUpgradeObservationBinding
	outcome    string
	returnedAt time.Time
}

func (a *movieUpgradeObservationAttempt) QueryMovieUpgrade(ctx context.Context, proof ports.MovieUpgradeNativeQueryProof) (ports.MovieUpgradeObservationProof, error) {
	a.mu.Lock()
	if a.closed || a.attempted {
		a.mu.Unlock()
		return nil, ports.ErrMovieUpgradeObservationReview
	}
	a.attempted, a.running = true, true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.running = false; a.mu.Unlock() }()
	binding, err := a.driver.verifier.ConsumeMovieUpgradeQuery(ctx, proof, a.descriptor)
	if err != nil {
		return nil, err
	}
	if binding.OperationID != a.descriptor.OperationID || binding.TaskID != a.descriptor.TaskID || binding.ObservationID == uuid.Nil || binding.Owner == uuid.Nil || binding.Token == uuid.Nil || len(binding.ObservationSHA256) != 64 || len(binding.PermitSHA256) != 64 {
		return nil, ports.ErrUpgradeReservationConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, queryErr := a.client.ListMovieUpgradeTorrents(ctx, a.descriptor.OperationTag)
	outcome := "unknown"
	if queryErr == nil && rows != nil {
		switch len(rows) {
		case 0:
			outcome = "absent"
		case 1:
			item := rows[0]
			if item != nil {
				tags := 0
				for _, token := range strings.Split(item.Tags, ",") {
					if strings.TrimSpace(token) == a.descriptor.OperationTag {
						tags++
					}
				}
				outcome = "ambiguous"
				if item.InfoHash == a.descriptor.InfoHash && item.SavePath == a.descriptor.SavePath && item.TotalBytes == a.descriptor.TotalBytes && tags == 1 && !strings.ContainsAny(item.Tags, "\x00\r\n") {
					outcome = "exact_match"
				}
			}
		default:
			outcome = "ambiguous"
		}
	}
	returned := &movieUpgradeObservationReturn{issuer: a, binding: binding, outcome: outcome, returnedAt: a.driver.clock.Now().UTC().Truncate(time.Microsecond)}
	a.mu.Lock()
	a.returned = returned
	a.mu.Unlock()
	return returned, nil
}
func (a *movieUpgradeObservationAttempt) VerifyMovieUpgradeObservation(ctx context.Context, proof ports.MovieUpgradeObservationProof, expected ports.MovieUpgradeObservationBinding) (ports.MovieUpgradeVerifiedObservation, error) {
	var zero ports.MovieUpgradeVerifiedObservation
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	p, ok := proof.(*movieUpgradeObservationReturn)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !ok || p == nil || a.closed || a.running || a.returned != p || p.issuer != a || p.binding != expected {
		return zero, ports.ErrUpgradeReservationConflict
	}
	return ports.MovieUpgradeVerifiedObservation{Binding: p.binding, Outcome: p.outcome, ReturnedAt: p.returnedAt}, nil
}
func (a *movieUpgradeObservationAttempt) Close(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return ports.ErrMovieUpgradeObservationReview
	}
	if !a.closed {
		a.closed = true
		a.release()
	}
	return nil
}
