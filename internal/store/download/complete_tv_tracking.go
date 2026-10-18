package download

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ObserveCompleteTVTracking records physical facts under a fresh observation lease.
// This explicit caller is not registered in the runtime worker yet. It cannot
// authorize Add, ingestion, or release.
// The repository is an explicit per-call dependency, independent of send services.
func (m *Manager) ObserveCompleteTVTracking(ctx context.Context, repository ports.CompleteTVTrackingRepository, inspector ports.TorrentMetainfoInspector, request ports.CompleteTVTrackingRequest) (out *ports.CompleteTVTrackingReceipt, err error) {
	defer func() {
		if err == nil {
			return
		}
		out = nil
		switch {
		case errors.Is(err, ports.ErrCompleteTVObservationStale), errors.Is(err, ports.ErrCompleteTVNeedsObservation):
		case errors.Is(err, context.Canceled):
			err = context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			err = context.DeadlineExceeded
		default:
			err = ports.ErrUnavailable
		}
	}()
	if request.TaskID == uuid.Nil || request.OperationID == uuid.Nil || request.Lease.ID == uuid.Nil || request.Lease.Owner == "" {
		return nil, ports.ErrCompleteTVObservationStale
	}
	m.mu.RLock()
	factory := m.factory
	m.mu.RUnlock()
	if repository == nil || inspector == nil || factory == nil {
		return nil, ports.ErrUnavailable
	}
	// This is the identical namespace and key calculation used by SendCompleteTV.
	// The dedicated advisory session never enters the pool. Its persistent permit
	// is removed only by this synchronous owner after native I/O and commit return.
	conn, e := pgx.ConnectConfig(ctx, m.pool.Config().ConnConfig.Copy())
	if e != nil {
		return nil, e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(cleanup)
	}()
	key := sha256.Sum256([]byte("pulsemedia.complete-tv.physical.v1:" + request.OperationID.String()))
	var held bool
	if e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, int64(binary.BigEndian.Uint64(key[:8]))).Scan(&held); e != nil {
		return nil, e
	}
	if !held {
		return nil, ports.ErrCompleteTVNeedsObservation
	}
	physicalRelease, e := m.acquireCompleteTVPhysicalPermit(ctx, request.OperationID, conn.PgConn().PID(), int64(binary.BigEndian.Uint64(key[:8])))
	if e != nil {
		return nil, e
	}
	defer physicalRelease()
	snapshot, e := repository.LoadCompleteTVTracking(ctx, request)
	if e != nil {
		return nil, e
	}
	if snapshot == nil || snapshot.Request != request || snapshot.ClientID == uuid.Nil || snapshot.ClientStamp == "" || snapshot.OperationTag != "pulsemedia:op:"+request.OperationID.String() {
		return nil, ports.ErrCompleteTVObservationStale
	}
	release, e := m.acquireClientBudget(ctx, snapshot.ClientID)
	if e != nil {
		return nil, e
	}
	defer release()
	// Revalidate lease and the full frozen snapshot after budget admission waits.
	fresh, e := repository.LoadCompleteTVTracking(ctx, request)
	if e != nil {
		return nil, e
	}
	if fresh == nil || *fresh != *snapshot {
		return nil, ports.ErrCompleteTVObservationStale
	}

	// The repository's short snapshot transaction has ended. Resolve the actual
	// configuration after admission, then bind that exact instance to its stamp.
	config, e := m.queries.GetDownloadClientByID(ctx, snapshot.ClientID)
	if e != nil {
		return nil, e
	}
	stamp := inventoryClientStamp(config)
	if stamp != snapshot.ClientStamp {
		return nil, ports.ErrCompleteTVObservationStale
	}
	password, e := crypto.Decrypt(m.masterKey, config.PasswordEncrypted)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	client, e := factory(config.ClientType, config.Url, config.Username, string(password))
	if e != nil || client == nil {
		return nil, ports.ErrUnavailable
	}
	capable, ok := client.(interface {
		CompleteTVTrackingObserver() ports.CompleteTVTrackingClient
	})
	if !ok {
		return nil, ports.ErrUnavailable
	}
	observer := capable.CompleteTVTrackingObserver()
	if observer == nil {
		return nil, ports.ErrUnavailable
	}
	// Check that the original session is alive immediately before native I/O too.
	if e = conn.Ping(ctx); e != nil {
		return nil, e
	}
	fresh, e = repository.LoadCompleteTVTracking(ctx, request)
	if e != nil {
		return nil, e
	}
	if fresh == nil || *fresh != *snapshot {
		return nil, ports.ErrCompleteTVObservationStale
	}
	before, e := observer.ListTorrents(ctx, snapshot.OperationTag)
	if e != nil {
		return nil, e
	}
	if before == nil {
		return nil, ports.ErrUnavailable
	}
	var files []ports.TorrentFile
	var metainfo *ports.CompleteTVMetainfoProof
	// Inventory is read only after a precise native completion result. The store
	// independently verifies identity and both sides of the inventory read.
	if len(before) == 1 && before[0] != nil && before[0].Progress == 1 && before[0].DownloadedBytes >= snapshot.TotalBytes {
		raw, readErr := observer.GetTorrentMetainfo(ctx, snapshot.InfoHash)
		if readErr != nil {
			return nil, readErr
		}
		metainfo, e = ports.InspectCompleteTVMetainfo(inspector, raw)
		if e != nil {
			return nil, e
		}
		files, e = observer.GetTorrentFiles(ctx, snapshot.InfoHash)
		if e != nil {
			return nil, e
		}
		if files == nil {
			return nil, ports.ErrUnavailable
		}
	}
	after, e := observer.ListTorrents(ctx, snapshot.OperationTag)
	if e != nil {
		return nil, e
	}
	if after == nil {
		return nil, ports.ErrUnavailable
	}
	remote := ports.CompleteTVTrackingObservation{Complete: true, ClientID: config.ID, ClientStamp: stamp, Before: before, After: after, Files: files, Metainfo: metainfo}
	// Only a complete successful network return may outlive logical cancellation
	// for bounded fact writeback. Neither physical permit is released before it.
	resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if e = conn.Ping(resultCtx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return repository.CommitCompleteTVTracking(resultCtx, *snapshot, remote)
}
