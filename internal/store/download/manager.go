package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
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
)

var (
	ErrPathMismatch     = ports.ErrDownloadPathMismatch
	ErrReconcilePending = ports.ErrDownloadReconcilePending
	ErrClientInUse      = ports.ErrDownloadClientInUse
)

type Manager struct {
	pool            *pgxpool.Pool
	idgen           ports.IDGen
	masterKey       []byte
	queries         *db.Queries
	clients         map[uuid.UUID]ports.DownloadClient
	slots           map[uuid.UUID]chan struct{}
	mu              sync.RWMutex
	factory         ClientFactory
	submissionStore ports.DownloadSubmissionRepository
	sourceFactory   ports.DownloadTorrentSourceFactory
	inspector       ports.TorrentMetainfoInspector
	indexerDecoder  ports.CredentialDecoder
	completeTV      *completeTVServices
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
		return false, "download client unavailable", err
	}
	defer release()
	client, err := m.clientFor(ctx, clientID)
	if err != nil {
		return false, "download client unavailable", err
	}
	if err := client.Test(ctx); err != nil {
		return false, "download client unavailable", err
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

// SubmitDownload submits a torrent to the download client, observing response loss recovery and reuse invariants.
func (m *Manager) SubmitDownload(ctx context.Context, opID, clientID, candID uuid.UUID, savePath string, authority ...ports.JobLease) (*db.DownloadTask, error) {
	if err := m.requireNoFrozenUpgrade(ctx, opID); err != nil {
		return nil, err
	}
	if err := m.requireGenericObservation(ctx, opID); err != nil {
		return nil, err
	}
	// 1. Check if task for this operation already exists (已存在任务复用且不改路径)
	existing, err := m.queries.GetDownloadTaskByOpID(ctx, opID)
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("failed to check existing download task: %w", err)
	}

	release, err := m.acquireClientBudget(ctx, clientID)
	if err != nil {
		return nil, err
	}
	defer release()

	// 2. Fetch candidate and decrypt download URL
	cand, err := m.queries.GetCandidateByID(ctx, candID)
	if err != nil {
		return nil, fmt.Errorf("candidate not found: %w", err)
	}

	decryptedURL, err := crypto.Decrypt(m.masterKey, cand.LinkEncrypted)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt download link: %w", err)
	}

	// Fetching is outside the transaction; final preparation revalidates the
	// candidate ciphertext, endpoint version, demand and job lease afterwards.
	payload, err := m.submissionPayload(ctx, cand, string(decryptedURL))
	if err != nil {
		return nil, err
	}
	infoHash := payload.InfoHash

	// 3. Create task record in submitting state
	taskID := m.idgen.NewUUID()
	params := db.CreateDownloadTaskParams{
		ID:          taskID,
		OperationID: opID,
		ClientID:    clientID,
		CandidateID: candID,
		InfoHash:    infoHash,
		State:       "submitting",
		SavePath:    savePath,
	}
	var task db.DownloadTask
	if len(authority) > 0 {
		if len(authority) != 1 || m.submissionStore == nil {
			return nil, ports.ErrDownloadDemandChanged
		}
		receipt, e := m.submissionStore.PrepareSubmission(ctx, ports.DownloadSubmissionCommand{Lease: authority[0], TaskID: taskID, OperationID: opID, ClientID: clientID, CandidateID: candID, InfoHash: infoHash, SavePath: savePath, CipherHash: sha256.Sum256(cand.LinkEncrypted)})
		if e != nil {
			return nil, e
		}
		if receipt.Declined {
			return nil, ports.ErrDownloadDemandChanged
		}
		task, err = m.queries.GetDownloadTaskByOpID(ctx, opID)
		if err == nil && receipt.AlreadyExisted {
			return &task, nil
		}
	} else {
		task, err = m.queries.CreateDownloadTask(ctx, params)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create download task: %w", err)
	}

	// Use persisted credentials when a client was not injected by the app.
	client, err := m.clientFor(ctx, clientID)
	if err != nil {
		_, _ = m.queries.TransitionTaskToUnknown(ctx, db.TransitionTaskToUnknownParams{ID: task.ID, Error: pgtype.Text{String: "download client unavailable", Valid: true}})
		return &task, err
	}

	tag := fmt.Sprintf("pulsemedia,pulsemedia:op:%s", opID)
	addErr := client.AddTorrent(ctx, ports.AddTorrentParam{
		InfoHash:     infoHash,
		DownloadURL:  payload.DownloadURL,
		TorrentBytes: payload.TorrentBytes,
		SavePath:     savePath,
		Tags:         tag,
		Paused:       false,
	})

	if len(authority) == 1 {
		if _, e := m.submissionStore.CompleteSubmission(ctx, authority[0], task.ID, addErr == nil); e != nil {
			return &task, e
		}
		updated, e := m.queries.GetDownloadTaskByOpID(ctx, opID)
		if e != nil {
			return &task, e
		}
		return &updated, addErr
	}
	if addErr != nil {
		// Response lost or network failure: transition to unknown (不换资源/释放，进入对账)
		updated, _ := m.queries.TransitionTaskToUnknown(ctx, db.TransitionTaskToUnknownParams{
			ID:    task.ID,
			Error: pgtype.Text{String: "downloader submission outcome unknown", Valid: true},
		})
		return &updated, addErr
	}

	// Submission confirmed: transition to tracking
	updated, err := m.queries.TransitionTaskToTracking(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to transition task to tracking: %w", err)
	}
	return &updated, nil
}

// ReconcileTask performs out-of-band reconciliation for unknown or submitting tasks.
func (m *Manager) ReconcileTask(ctx context.Context, opID uuid.UUID) (*db.DownloadTask, error) {
	if err := m.requireNoFrozenUpgrade(ctx, opID); err != nil {
		return nil, err
	}
	task, err := m.queries.GetDownloadTaskByOpID(ctx, opID)
	if err != nil {
		return nil, fmt.Errorf("failed to load download task: %w", err)
	}
	if err = m.requireGenericObservation(ctx, opID); err != nil {
		return nil, err
	}
	if task.State != "submitting" && task.State != "unknown" {
		return &task, nil
	}

	release, err := m.acquireClientBudget(ctx, task.ClientID)
	if err != nil {
		return nil, err
	}
	defer release()

	client, err := m.clientFor(ctx, task.ClientID)
	if err != nil {
		return nil, err
	}

	var info *ports.TorrentInfo
	if task.InfoHash != "" {
		info, err = client.GetTorrent(ctx, task.InfoHash)
	} else {
		// If the indexer did not provide a hash, only an exact operation tag
		// and a single externally observed torrent may establish identity.
		tag := "pulsemedia:op:" + opID.String()
		var matches []*ports.TorrentInfo
		matches, err = client.ListTorrents(ctx, tag)
		if err == nil {
			matched := 0
			for _, candidate := range matches {
				if candidate == nil {
					continue
				}
				for _, t := range strings.Split(candidate.Tags, ",") {
					if strings.TrimSpace(t) == tag {
						matched++
						info = candidate
						break
					}
				}
			}
			if matched != 1 {
				info = nil
			}
		}
		if err == nil && info != nil {
			hash := strings.ToLower(info.InfoHash)
			_, decodeErr := hex.DecodeString(hash)
			if (len(hash) != 40 && len(hash) != 64) || decodeErr != nil {
				info = nil
			} else {
				task, err = m.queries.SetObservedDownloadInfoHash(ctx, db.SetObservedDownloadInfoHashParams{ID: task.ID, InfoHash: hash})
			}
		}
	}
	if err != nil {
		if !errors.Is(err, ports.ErrNotFound) {
			return nil, err
		}
	}
	if info == nil || (task.InfoHash != "" && !strings.EqualFold(info.InfoHash, task.InfoHash)) {
		// No observation (or the adapter returned the wrong torrent): keep the
		// reservation and await operator review, never infer that Add failed.
		_, _ = m.queries.IncrementReconcileCount(ctx, task.ID)
		return &task, ErrReconcilePending
	}

	// Verify save path matches (已存在任务复用且不改路径)
	if !sameOrChildPath(task.SavePath, info.SavePath) ||
		(info.ContentPath != "" && !sameOrChildPath(task.SavePath, info.ContentPath)) {
		_, _ = m.recordObservationState(ctx, task, "unknown", "downloader save path mismatch")
		return nil, ErrPathMismatch
	}

	if math.IsNaN(float64(info.Progress)) || math.IsInf(float64(info.Progress), 0) || info.Progress < 0 || info.Progress > 1 || info.TotalBytes < 0 || info.DownloadedBytes < 0 {
		return nil, ErrReconcilePending
	}
	// Successfully reconciled: transition to tracking
	_, err = m.recordObservationState(ctx, task, "tracking", "")
	if err != nil {
		return nil, fmt.Errorf("failed to recover task to tracking: %w", err)
	}

	// Update latest progress
	finalTask, err := m.queries.UpdateDownloadTaskProgress(ctx, db.UpdateDownloadTaskProgressParams{
		ID:              task.ID,
		State:           "tracking",
		Progress:        info.Progress,
		TotalBytes:      info.TotalBytes,
		DownloadedBytes: info.DownloadedBytes,
		ContentPath:     pgtype.Text{String: info.ContentPath, Valid: info.ContentPath != ""},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update reconciled task progress: %w", err)
	}

	return &finalTask, nil
}

// ObserveTask refreshes a tracking download without claiming its files have
// been imported. Missing torrents and path changes retain the business
// reservation and require reconciliation; progress alone cannot release it.
func (m *Manager) ObserveTask(ctx context.Context, opID uuid.UUID) (*db.DownloadTask, error) {
	if err := m.requireNoFrozenUpgrade(ctx, opID); err != nil {
		return nil, err
	}
	task, err := m.queries.GetDownloadTaskByOpID(ctx, opID)
	if err != nil {
		return nil, err
	}
	if err = m.requireGenericObservation(ctx, opID); err != nil {
		return nil, err
	}
	if task.State != "tracking" {
		return &task, nil
	}
	release, err := m.acquireClientBudget(ctx, task.ClientID)
	if err != nil {
		return nil, err
	}
	defer release()

	configSnapshot, err := m.queries.GetDownloadClientByID(ctx, task.ClientID)
	if err != nil {
		return nil, ports.ErrUnavailable
	}
	client, err := m.clientFor(ctx, task.ClientID)
	if err != nil {
		return nil, err
	}
	info, err := client.GetTorrent(ctx, task.InfoHash)
	if errors.Is(err, ports.ErrNotFound) {
		_, err = m.recordObservationState(ctx, task, "unknown", "torrent no longer visible; manual reconciliation required")
		if err != nil {
			return nil, err
		}
		return nil, ErrReconcilePending
	}
	if err != nil {
		return nil, err
	}
	if info == nil || task.InfoHash == "" || !strings.EqualFold(info.InfoHash, task.InfoHash) ||
		!sameOrChildPath(task.SavePath, info.SavePath) ||
		(info.ContentPath != "" && !sameOrChildPath(task.SavePath, info.ContentPath)) ||
		math.IsNaN(float64(info.Progress)) || math.IsInf(float64(info.Progress), 0) || info.Progress < 0 || info.Progress > 1 || info.TotalBytes < 0 || info.DownloadedBytes < 0 {
		_, err = m.recordObservationState(ctx, task, "unknown", "download identity or path requires review")
		if err != nil {
			return nil, err
		}
		return nil, ErrReconcilePending
	}
	if completedObservation(info) {
		inventory, e := m.observeEpisodeInventory(ctx, task, client, info)
		if e != nil {
			return nil, e
		}
		if inventory == nil && info.ContentPath != "" && !naming.IsVideoPath(info.ContentPath) {
			inventory, e = m.observeMovieDirectory(ctx, task, client, info, inventoryClientStamp(configSnapshot))
			if e != nil {
				return nil, e
			}
		}
		return m.completeForOrganization(ctx, task, info, inventory)
	}
	observed, err := m.queries.UpdateDownloadTaskProgress(ctx, db.UpdateDownloadTaskProgressParams{
		ID: task.ID, State: "tracking", Progress: info.Progress,
		TotalBytes: info.TotalBytes, DownloadedBytes: info.DownloadedBytes,
		ContentPath: pgtype.Text{String: info.ContentPath, Valid: info.ContentPath != ""},
	})
	if err != nil {
		return nil, err
	}
	return &observed, nil
}

// TranslateRemotePath maps remote container paths to local storage paths.
func TranslateRemotePath(remotePathMap map[string]string, remotePath string) string {
	return naming.TranslateRemotePath(remotePathMap, remotePath)
}
