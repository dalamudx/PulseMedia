package download

import (
	"context"
	"errors"
	"slices"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

// ObserveAutomaticTV records original physical facts under a fresh read-only
// lease. The repository is an explicit per-call dependency.
func (m *Manager) ObserveAutomaticTV(ctx context.Context, repository ports.AutomaticTVObservationRepository, request ports.AutomaticTVObservationRequest) (out *ports.AutomaticTVObservationReceipt, err error) {
	defer func() {
		if err == nil {
			return
		}
		out = nil
		switch {
		case errors.Is(err, ports.ErrAutomaticTVObservationStale), errors.Is(err, ports.ErrAutomaticTVNeedsObservation):
		case errors.Is(err, context.Canceled):
			err = context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			err = context.DeadlineExceeded
		default:
			err = ports.ErrUnavailable
		}
	}()
	if request.TaskID == uuid.Nil || request.OperationID == uuid.Nil || request.Lease.ID == uuid.Nil || request.Lease.Owner == "" || request.Lease.Token <= 0 || request.PhysicalPermitID != uuid.Nil {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if m == nil || m.pool == nil || m.queries == nil || m.idgen == nil || repository == nil {
		return nil, ports.ErrUnavailable
	}
	m.mu.RLock()
	factory := m.factory
	m.mu.RUnlock()
	if factory == nil {
		return nil, ports.ErrUnavailable
	}
	conn, permit, physicalRelease, e := m.acquireAutomaticTVObservationPhysical(ctx, request)
	if e != nil {
		return nil, e
	}
	defer physicalRelease()
	request.PhysicalPermitID = permit
	snapshot, e := repository.LoadAutomaticTVObservation(ctx, request)
	if e != nil {
		return nil, e
	}
	if snapshot == nil || snapshot.Request != request || snapshot.Authority.OperationID != request.OperationID || snapshot.SendPhysicalAttemptID == uuid.Nil || snapshot.ClientID == uuid.Nil || snapshot.ClientID != snapshot.Authority.Target.ClientID || snapshot.ClientStamp == "" || snapshot.ClientStamp != snapshot.Authority.Target.ClientStamp || snapshot.InfoHash == "" || snapshot.SavePath == "" || snapshot.TotalBytes <= 0 || snapshot.Seal == "" || snapshot.OperationTag != "pulsemedia:op:"+request.OperationID.String() {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	task, e := m.queries.GetDownloadTaskByOpID(ctx, request.OperationID)
	if e != nil {
		return nil, e
	}
	if task.ID != request.TaskID || task.OperationID != request.OperationID || task.CandidateID != snapshot.Authority.CandidateID || task.ClientID != snapshot.ClientID || task.InfoHash != snapshot.InfoHash || task.SavePath != snapshot.SavePath || (task.State != "submitting" && task.State != "unknown" && task.State != "tracking") {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	release, e := m.acquireClientBudget(ctx, snapshot.ClientID)
	if e != nil {
		return nil, e
	}
	defer release()
	// Load's transaction is over; bind a fresh client to the actual current row.
	config, e := m.queries.GetDownloadClientByID(ctx, snapshot.ClientID)
	if e != nil {
		return nil, e
	}
	stamp := inventoryClientStamp(config)
	if stamp != snapshot.ClientStamp {
		return nil, ports.ErrAutomaticTVObservationStale
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
		AutomaticTVObserver() ports.AutomaticTVReadClient
	})
	if !ok {
		return nil, ports.ErrUnavailable
	}
	observer := capable.AutomaticTVObserver()
	if observer == nil {
		return nil, ports.ErrUnavailable
	}
	if e = conn.Ping(ctx); e != nil {
		return nil, e
	}
	before, e := observer.ListAutomaticTVTasks(ctx, snapshot.OperationTag)
	// Await the real call even if it ignores cancellation. A late success cannot
	// become evidence or release either physical owner while I/O is outstanding.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if e != nil {
		return nil, e
	}
	if before == nil {
		return nil, ports.ErrUnavailable
	}
	var files []ports.AutomaticTVRemoteFile
	if len(before) == 1 {
		a := before[0]
		if a.Complete && a.InfoHash == snapshot.InfoHash && a.SavePath == snapshot.SavePath && a.TotalBytes == snapshot.TotalBytes && a.VerifiedBytes == a.TotalBytes && slices.Contains(a.Tags, snapshot.OperationTag) {
			if e = conn.Ping(ctx); e != nil {
				return nil, e
			}
			files, e = observer.GetAutomaticTVFiles(ctx, snapshot.InfoHash)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if e != nil {
				return nil, e
			}
			if files == nil {
				return nil, ports.ErrUnavailable
			}
		}
	}
	if e = conn.Ping(ctx); e != nil {
		return nil, e
	}
	after, e := observer.ListAutomaticTVTasks(ctx, snapshot.OperationTag)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if e != nil {
		return nil, e
	}
	if after == nil {
		return nil, ports.ErrUnavailable
	}
	if e = conn.Ping(ctx); e != nil {
		return nil, e
	}
	out, e = repository.CommitAutomaticTVObservation(ctx, *snapshot, ports.AutomaticTVDownloadObservation{Complete: true, ClientID: config.ID, ClientStamp: stamp, Before: before, After: after, Files: files})
	if e != nil {
		return nil, e
	}
	if out == nil || out.TaskID != request.TaskID {
		return nil, ports.ErrAutomaticTVObservationStale
	}
	return out, nil
}
