package download

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ObserveCompleteTV records physical facts under a fresh observation lease.
// The controlled worker consumes it; it cannot authorize Add or ingestion.
// The repository is an explicit per-call dependency, independent of send services.
func (m *Manager) ObserveCompleteTV(ctx context.Context, repository ports.CompleteTVObservationRepository, request ports.CompleteTVObservationRequest) (out *ports.CompleteTVObservationReceipt, err error) {
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
	if repository == nil || factory == nil {
		return nil, ports.ErrUnavailable
	}
	// This is the identical namespace and key calculation used by SendCompleteTV.
	// Closing the dedicated session releases the permit; it never enters the pool.
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
	snapshot, e := repository.LoadCompleteTVObservation(ctx, request)
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
	capable, ok := client.(interface{ CompleteTVObserver() ports.DownloadClient })
	if !ok {
		return nil, ports.ErrUnavailable
	}
	observer := capable.CompleteTVObserver()
	if observer == nil {
		return nil, ports.ErrUnavailable
	}
	// Check that the original session is alive immediately before native I/O too.
	if e = conn.Ping(ctx); e != nil {
		return nil, e
	}
	items, e := observer.ListTorrents(ctx, snapshot.OperationTag)
	if e != nil {
		return nil, e
	}
	// nil is indeterminate, while a non-nil empty complete list proves no match.
	if items == nil {
		return nil, ports.ErrUnavailable
	}
	remote := ports.CompleteTVRemoteObservation{Complete: true, ClientID: config.ID, ClientStamp: stamp, Matches: make([]ports.CompleteTVRemoteMatch, 0, len(items))}
	for _, item := range items {
		if item == nil || item.InfoHash == "" || item.SavePath == "" || item.TotalBytes < 0 || strings.ContainsAny(item.Tags, "\x00\r\n") {
			return nil, ports.ErrUnavailable
		}
		tag := ""
		for _, token := range strings.Split(item.Tags, ",") {
			if strings.TrimSpace(token) == snapshot.OperationTag {
				tag = snapshot.OperationTag
			}
		}
		// Never filter returned rows by hash or tag: mismatches and ambiguity are
		// evidence requiring review, not absence or permission to select one row.
		remote.Matches = append(remote.Matches, ports.CompleteTVRemoteMatch{InfoHash: item.InfoHash, OperationTag: tag, SavePath: item.SavePath, TotalBytes: item.TotalBytes})
	}
	// Only a complete successful network return may outlive logical cancellation
	// for bounded fact writeback. Neither physical permit is released before it.
	resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if e = conn.Ping(resultCtx); e != nil {
		return nil, ports.ErrUnavailable
	}
	return repository.CommitCompleteTVObservation(resultCtx, *snapshot, remote)
}
