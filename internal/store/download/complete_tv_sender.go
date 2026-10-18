package download

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type completeTVServices struct {
	preparation ports.CompleteTVSubmissionRepository
	results     ports.CompleteTVSubmissionResultRepository
	fetcher     ports.CompleteTVTorrentFetcher
	inspector   ports.TorrentMetainfoInspector
}

// SetCompleteTVServices configures the dedicated submission path at composition time.
func (m *Manager) SetCompleteTVServices(preparation ports.CompleteTVSubmissionRepository, results ports.CompleteTVSubmissionResultRepository, fetcher ports.CompleteTVTorrentFetcher, inspector ports.TorrentMetainfoInspector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completeTV = &completeTVServices{preparation: preparation, results: results, fetcher: fetcher, inspector: inspector}
}

// SendCompleteTV is deliberately not registered with any worker or public route.
// The caller owns the synchronous invocation through physical HTTP return. A
// preparation is a one-use send fence, never evidence that Add did not happen.
func (m *Manager) SendCompleteTV(ctx context.Context, lease ports.JobLease, operation uuid.UUID) (out *ports.CompleteTVSubmissionResultReceipt, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ports.ErrCompleteTVNeedsObservation) && !errors.Is(err, ports.ErrCompleteTVSubmissionReview) && !errors.Is(err, ports.ErrCompleteTVResultStale) {
			if errors.Is(err, context.Canceled) {
				err = context.Canceled
			} else if errors.Is(err, context.DeadlineExceeded) {
				err = context.DeadlineExceeded
			} else {
				err = ports.ErrUnavailable
			}
		}
	}()
	if operation == uuid.Nil || lease.ID == uuid.Nil || lease.Owner == "" {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	m.mu.RLock()
	services, factory := m.completeTV, m.factory
	m.mu.RUnlock()
	if services == nil || services.preparation == nil || services.results == nil || services.fetcher == nil || services.inspector == nil || factory == nil {
		return nil, ports.ErrUnavailable
	}
	// A dedicated session is closed, never returned to a pool with an advisory
	// lock. Its lifetime is independent of the caller cancellation after admission.
	conn, e := pgx.ConnectConfig(ctx, m.pool.Config().ConnConfig.Copy())
	if e != nil {
		return nil, e
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	key := sha256.Sum256([]byte("pulsemedia.complete-tv.physical.v1:" + operation.String()))
	var held bool
	if e = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::bigint)`, int64(binary.BigEndian.Uint64(key[:8]))).Scan(&held); e != nil {
		return nil, e
	}
	if !held {
		return nil, ports.ErrCompleteTVNeedsObservation
	}
	physicalRelease, e := m.acquireCompleteTVPhysicalPermit(ctx, operation, conn.PgConn().PID(), int64(binary.BigEndian.Uint64(key[:8])))
	if e != nil {
		return nil, e
	}
	defer physicalRelease()
	work, e := m.readCompleteTVSendWork(ctx, lease, operation)
	if e != nil {
		return nil, e
	}
	release, e := m.acquireClientBudget(ctx, work.frozen.ClientID)
	if e != nil {
		return nil, e
	}
	defer release()
	// Never use clientFor: an injected/cached client may refer to old credentials.
	password, e := crypto.Decrypt(m.masterKey, work.client.PasswordEncrypted)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	client, e := factory(work.client.ClientType, work.client.Url, work.client.Username, string(password))
	if e != nil || client == nil {
		return nil, ports.ErrUnavailable
	}
	// Only adapters with exact bytes, stable tags and affirmative Add semantics
	// may opt in. Generic Add implementations do not establish that contract.
	capable, ok := client.(interface{ CompleteTVByteSender() ports.DownloadClient })
	if !ok {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	client = capable.CompleteTVByteSender()
	if client == nil {
		return nil, ports.ErrUnavailable
	}
	link, e := crypto.Decrypt(m.masterKey, work.candidate.LinkEncrypted)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	apiKey, e := crypto.Decrypt(m.masterKey, work.indexer.ApiKeyEncrypted)
	if e != nil {
		return nil, ports.ErrUnavailable
	}
	// Native Torznab fetch is bounded to 10MiB/15 seconds and refuses redirects.
	// No retry, magnet fallback, URL submission, or metainfo reencoding exists here.
	raw, e := services.fetcher.FetchCompleteTVTorrent(ctx, ports.CompleteTVTorrentSource{EndpointURL: work.indexer.Url, APIKey: string(apiKey), DownloadURL: string(link)})
	if e != nil {
		return nil, e
	}
	proof, e := ports.InspectCompleteTVMetainfo(services.inspector, raw)
	if e != nil {
		return nil, e
	}
	task := m.idgen.NewUUID()
	prepared, e := services.preparation.PrepareCompleteTVSubmission(ctx, ports.PrepareCompleteTVSubmissionCommand{Lease: lease, TaskID: task, OperationID: operation, Metainfo: proof})
	if e != nil {
		return nil, e
	}
	if prepared == nil || prepared.AlreadyExisted {
		return nil, ports.ErrCompleteTVNeedsObservation
	}
	if prepared.TaskID != task || prepared.State != "prepared_not_sent" {
		return nil, ports.ErrCompleteTVNeedsObservation
	}
	// The stored task is the authoritative original path bound by v2. A root
	// edit during fetching must not send the earlier reader path.
	persisted, e := m.queries.GetDownloadTaskByOpID(ctx, operation)
	if e != nil {
		return nil, e
	}
	if persisted.ID != task || persisted.SavePath != work.savePath || persisted.InfoHash != proof.InfoHash() {
		return nil, ports.ErrCompleteTVNeedsObservation
	}
	// Preparation checks this exact client stamp and root path against the frozen
	// authority. Configuration changes after that commit cannot swap the instance.
	addErr := client.AddTorrent(ctx, ports.AddTorrentParam{TorrentBytes: proof.TorrentBytes(), InfoHash: proof.InfoHash(), SavePath: work.savePath, Tags: "pulsemedia,pulsemedia:op:" + operation.String()})
	// Keep both physical permits until Add actually returns, including adapters
	// which ignore cancellation. Writeback has a separate bounded live context;
	// original lease expiry still fails closed in the repository.
	resultCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return services.results.RecordCompleteTVSubmissionResult(resultCtx, ports.CompleteTVSubmissionResultCommand{Lease: lease, TaskID: task, OperationID: operation, Metainfo: proof, Acknowledged: addErr == nil, AddError: addErr})
}

type completeTVSendWork struct {
	frozen    ports.CompleteTVCoverageExecutionSnapshot
	candidate db.ReleaseCandidate
	client    db.DownloadClient
	indexer   db.Indexer
	savePath  string
}

// Read a coherent frozen work bundle without locks or network inside a business
// transaction. The preparation transaction is the final authorization boundary.
func (m *Manager) readCompleteTVSendWork(ctx context.Context, lease ports.JobLease, operation uuid.UUID) (*completeTVSendWork, error) {
	tx, e := m.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var existing bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM download_task WHERE operation_id=$1) OR EXISTS(SELECT 1 FROM complete_tv_submission_preparation WHERE operation_id=$1)`, operation).Scan(&existing); e != nil {
		return nil, e
	}
	if existing {
		return nil, ports.ErrCompleteTVNeedsObservation
	}
	q := db.New(tx)
	record, e := q.GetCompleteTVExecutionAuthority(ctx, operation)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	if e != nil {
		return nil, e
	}
	w := new(completeTVSendWork)
	if json.Unmarshal(record.Snapshot, &w.frozen) != nil || w.frozen.Kind != "reviewed_complete_tv_coverage_v1" || w.frozen.ClientID != record.ClientID || w.frozen.CandidateID != record.CandidateID || w.frozen.RootID != record.RootID || len(w.frozen.Units) < 2 {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	job, e := q.GetAuthorizedCompleteTVPreparationJob(ctx, db.GetAuthorizedCompleteTVPreparationJobParams{ID: lease.ID, Owner: lease.Owner, Token: lease.Token})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	if e != nil {
		return nil, e
	}
	var payload struct {
		Operation uuid.UUID `json:"operation_id"`
		Authority uuid.UUID `json:"complete_tv_authority"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil || payload.Operation != operation || payload.Authority != operation {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	w.client, e = q.GetDownloadClientByID(ctx, w.frozen.ClientID)
	if e != nil {
		return nil, e
	}
	if inventoryClientStamp(w.client) != w.frozen.ClientStamp {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	root, e := q.GetLibraryRootByID(ctx, w.frozen.RootID)
	if e != nil {
		return nil, e
	}
	w.savePath = root.Path
	if root.Generation != w.frozen.RootGeneration || w.savePath == "" {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	w.candidate, e = q.GetCandidateByID(ctx, w.frozen.CandidateID)
	if e != nil {
		return nil, e
	}
	if quality.AcquisitionSourceStamp(w.candidate.Title, w.candidate.InfoHash.String, w.candidate.LinkEncrypted, w.candidate.QualityProfile) != w.frozen.SourceStamp {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	w.indexer, e = q.GetIndexerByID(ctx, w.candidate.IndexerID)
	if e != nil {
		return nil, e
	}
	if w.indexer.ConfigVersion != w.candidate.SourceConfigVersion {
		return nil, ports.ErrCompleteTVSubmissionReview
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, e
	}
	return w, nil
}
