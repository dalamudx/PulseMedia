package download

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	downloadcase "github.com/dalamudx/PulseMedia/internal/usecase/download"
)

var (
	ErrPathMismatch     = ports.ErrDownloadPathMismatch
	ErrReconcilePending = ports.ErrDownloadReconcilePending
	ErrClientInUse      = ports.ErrDownloadClientInUse
)

type Manager struct {
	pool              *pgxpool.Pool
	idgen             ports.IDGen
	masterKey         []byte
	queries           *db.Queries
	clients           map[uuid.UUID]ports.DownloadClient
	slots             map[uuid.UUID]chan struct{}
	mu                sync.RWMutex
	factory           ClientFactory
	submissionStore   ports.DownloadSubmissionRepository
	sourceFactory     ports.DownloadTorrentSourceFactory
	inspector         ports.TorrentMetainfoInspector
	indexerDecoder    ports.CredentialDecoder
	completeTV        *completeTVServices
	automaticTV       *automaticTVServices
	recordRates       bool
	trackingHintFiles ports.RootFilesystem
}

func NewManager(pool *pgxpool.Pool, idgen ports.IDGen, masterKey []byte) *Manager {
	return &Manager{
		pool:      pool,
		idgen:     idgen,
		masterKey: masterKey,
		queries:   postgres.Queries(pool),
		clients:   make(map[uuid.UUID]ports.DownloadClient),
		slots:     make(map[uuid.UUID]chan struct{}),
	}
}

func (m *Manager) SetSubmissionRepository(repo ports.DownloadSubmissionRepository) {
	m.submissionStore = repo
}

func (m *Manager) RegisterClient(clientID uuid.UUID, client ports.DownloadClient) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients[clientID] = client
}

type ClientFactory = ports.DownloadClientFactory

func (m *Manager) SetClientFactory(f ClientFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.factory = f
}

// DeleteClient refuses to remove a client while any subscription or task still
// references it. FK errors from concurrent inserts also count as conflicts.
func (m *Manager) DeleteClient(ctx context.Context, id uuid.UUID) error {
	if _, err := m.queries.GetDownloadClientByID(ctx, id); err != nil {
		return err
	}
	n, err := m.queries.DeleteDownloadClientIfUnused(ctx, id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrClientInUse
		}
		return err
	}
	if n == 0 {
		return ErrClientInUse
	}
	m.mu.Lock()
	delete(m.clients, id)
	m.mu.Unlock()
	return nil
}

// clientFor reloads encrypted credentials after restart or configuration changes.
func (m *Manager) clientFor(ctx context.Context, id uuid.UUID) (ports.DownloadClient, error) {
	m.mu.RLock()
	client := m.clients[id]
	factory := m.factory
	m.mu.RUnlock()
	if client != nil {
		return client, nil
	}
	if factory == nil {
		return nil, errors.New("download client not registered")
	}
	row, err := m.queries.GetDownloadClientByID(ctx, id)
	if err != nil {
		return nil, err
	}
	plain, err := crypto.Decrypt(m.masterKey, row.PasswordEncrypted)
	if err != nil {
		return nil, errors.New("download client credentials unavailable")
	}
	return factory(row.ClientType, row.Url, row.Username, string(plain))
}

// TestClient tests connectivity to the download client.
func (m *Manager) TestClient(ctx context.Context, clientID uuid.UUID) (bool, string, error) {
	release, err := m.acquireClientBudget(ctx, clientID)
	if err != nil {
		if ctx.Err() != nil {
			return false, "download client unavailable", ctx.Err()
		}
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ports.ErrNotFound) {
			return false, "download client not found", ports.ErrNotFound
		}
		return false, "download client unavailable", ports.ErrUnavailable
	}
	defer release()
	client, err := m.clientFor(ctx, clientID)
	if err != nil {
		if ctx.Err() != nil {
			return false, "download client unavailable", ctx.Err()
		}
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ports.ErrNotFound) {
			return false, "download client not found", ports.ErrNotFound
		}
		return false, "download client unavailable", ports.ErrUnavailable
	}
	if client == nil {
		return false, "download client unavailable", ports.ErrUnavailable
	}
	if ctx.Err() != nil {
		return false, "download client unavailable", ctx.Err()
	}
	if err := client.Test(ctx); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return false, "download client unavailable", ctx.Err()
		}
		return false, "download client unavailable", ports.ErrEndpointProbeFailed
	}
	if ctx.Err() != nil {
		return false, "download client unavailable", ctx.Err()
	}
	return true, "ok", nil
}

// sameOrChildPath checks path boundaries rather than matching an untrusted string prefix.
func sameOrChildPath(root, actual string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(actual) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(actual))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PrepareDownload freezes the original task and connection and retains the
// physical budget. The usecase owns Add and calls Complete before Release.
func (m *Manager) PrepareDownload(ctx context.Context, opID, clientID, candID uuid.UUID, savePath string, authority ...ports.JobLease) (*ports.PreparedDownload, error) {
	if err := m.requireNoFrozenUpgrade(ctx, opID); err != nil {
		return nil, err
	}
	if err := m.requireGenericObservation(ctx, opID); err != nil {
		return nil, err
	}
	existing, err := m.queries.GetDownloadTaskByOpID(ctx, opID)
	if err == nil {
		return preparedTask(&existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("failed to check existing download task: %w", err)
	}

	release, err := m.acquireClientBudget(ctx, clientID)
	if err != nil {
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			release()
		}
	}()
	cand, err := m.queries.GetCandidateByID(ctx, candID)
	if err != nil {
		return nil, fmt.Errorf("candidate not found: %w", err)
	}
	decryptedURL, err := crypto.Decrypt(m.masterKey, cand.LinkEncrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt download link: %w", err)
	}
	// External source reads stay outside the transaction; preparation revalidates
	// the ciphertext, endpoint, demand and lease after fetching.
	payload, err := m.submissionPayload(ctx, cand, string(decryptedURL))
	if err != nil {
		return nil, err
	}
	infoHash := payload.InfoHash
	var clientSnapshot *db.DownloadClient
	clientStamp := ""
	if len(authority) == 1 {
		bound, e := m.queries.GetBoundSubmissionClient(ctx, clientID)
		if e != nil {
			return nil, ports.ErrUnavailable
		}
		clientSnapshot, clientStamp = &bound.DownloadClient, bound.ConfigStamp
	}
	taskID := m.idgen.NewUUID()
	var task db.DownloadTask
	if len(authority) > 0 {
		if len(authority) != 1 || m.submissionStore == nil {
			return nil, ports.ErrDownloadDemandChanged
		}
		receipt, e := m.submissionStore.PrepareSubmission(ctx, ports.DownloadSubmissionCommand{Lease: authority[0], TaskID: taskID, OperationID: opID, ClientID: clientID, CandidateID: candID, InfoHash: infoHash, SavePath: savePath, CipherHash: sha256.Sum256(cand.LinkEncrypted), ClientConfigStamp: clientStamp})
		if e != nil {
			return nil, e
		}
		if receipt.Declined {
			return nil, ports.ErrDownloadDemandChanged
		}
		task, err = m.queries.GetDownloadTaskByOpID(ctx, opID)
		if err == nil && receipt.AlreadyExisted {
			return preparedTask(&task), nil
		}
	} else {
		task, err = m.queries.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: taskID, OperationID: opID, ClientID: clientID, CandidateID: candID, InfoHash: infoHash, State: "submitting", SavePath: savePath})
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create download task: %w", err)
	}
	var client ports.DownloadClient
	if clientSnapshot != nil {
		client, err = m.clientForRateSnapshot(ctx, *clientSnapshot)
	} else {
		client, err = m.clientFor(ctx, clientID)
	}
	if err != nil {
		_, _ = m.queries.TransitionTaskToUnknown(ctx, db.TransitionTaskToUnknownParams{ID: task.ID, Error: pgtype.Text{String: "download client unavailable", Valid: true}})
		return preparedTask(&task), err
	}
	prepared := preparedTask(&task)
	prepared.Client = client
	prepared.Params = ports.AddTorrentParam{InfoHash: infoHash, DownloadURL: payload.DownloadURL, TorrentBytes: payload.TorrentBytes, SavePath: savePath, Tags: fmt.Sprintf("pulsemedia,pulsemedia:op:%s", opID)}
	frozenAuthority := append([]ports.JobLease(nil), authority...)
	prepared.Complete = func(c context.Context, addErr error) (*ports.DownloadExecutionTask, error) {
		return taskDTO(m.completeDownload(c, task, addErr, frozenAuthority...))
	}
	prepared.Release = release
	retained = true
	return prepared, nil
}

func preparedTask(task *db.DownloadTask) *ports.PreparedDownload {
	dto, _ := taskDTO(task, nil)
	return &ports.PreparedDownload{Task: dto}
}

func (m *Manager) completeDownload(ctx context.Context, task db.DownloadTask, addErr error, authority ...ports.JobLease) (*db.DownloadTask, error) {
	if len(authority) == 1 {
		if _, e := m.submissionStore.CompleteSubmission(ctx, authority[0], task.ID, addErr == nil); e != nil {
			return &task, e
		}
		updated, e := m.queries.GetDownloadTaskByOpID(ctx, task.OperationID)
		if e != nil {
			return &task, e
		}
		return &updated, addErr
	}
	if addErr != nil {
		updated, err := m.queries.TransitionTaskToUnknown(ctx, db.TransitionTaskToUnknownParams{ID: task.ID, Error: pgtype.Text{String: "downloader submission outcome unknown", Valid: true}})
		if err != nil {
			return &task, err
		}
		return &updated, addErr
	}
	updated, err := m.queries.TransitionTaskToTracking(ctx, task.ID)
	if err != nil {
		return &task, fmt.Errorf("failed to transition task to tracking: %w", err)
	}
	return &updated, nil
}

// Compatibility entry points delegate orchestration to the same usecase as App.
func (m *Manager) ReconcileTask(ctx context.Context, opID uuid.UUID) (*db.DownloadTask, error) {
	task, err := downloadcase.ReconcileTask(ctx, m, opID)
	return m.observationTask(ctx, task, err)
}
func (m *Manager) observeTask(ctx context.Context, opID uuid.UUID, watch *ports.DownloadWatchTarget) (*db.DownloadTask, error) {
	var task *ports.DownloadExecutionTask
	var err error
	if watch == nil {
		task, err = downloadcase.ObserveTask(ctx, m, opID)
	} else {
		task, err = downloadcase.ObserveDownloadWatchTarget(ctx, m, *watch)
	}
	return m.observationTask(ctx, task, err)
}
func (m *Manager) observationTask(ctx context.Context, task *ports.DownloadExecutionTask, err error) (*db.DownloadTask, error) {
	if task == nil {
		return nil, err
	}
	row, e := m.queries.GetDownloadTaskByOpID(ctx, task.OperationID)
	if e != nil {
		return nil, e
	}
	return &row, err
}

// TranslateRemotePath maps remote container paths to local storage paths.
func TranslateRemotePath(remotePathMap map[string]string, remotePath string) string {
	return naming.TranslateRemotePath(remotePathMap, remotePath)
}
