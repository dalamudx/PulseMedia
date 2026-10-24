//go:build linux

package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Drop the server's successful COMMIT frame, after independently observing the ledger.
// Only this test's runtime connections are wrapped; the assertion pool stays direct.
type m8CommitReceiptConn struct {
	net.Conn
	mu        sync.Mutex
	pending   []byte
	committed func() bool
	dropped   *atomic.Int32
}

func (c *m8CommitReceiptConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.pending) == 0 {
		header := make([]byte, 5)
		if _, err := io.ReadFull(c.Conn, header); err != nil {
			return 0, err
		}
		length := binary.BigEndian.Uint32(header[1:])
		if length < 4 || length > 32<<20 {
			return 0, io.ErrUnexpectedEOF
		}
		body := make([]byte, int(length)-4)
		if _, err := io.ReadFull(c.Conn, body); err != nil {
			return 0, err
		}
		if header[0] == 'C' && bytes.Equal(body, []byte("COMMIT\x00")) && c.dropped.Load() == 0 && c.committed() && c.dropped.CompareAndSwap(0, 1) {
			_ = c.Close()
			return 0, io.ErrUnexpectedEOF
		}
		c.pending = append(header, body...)
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func TestM8UntrackedOrganizationCommitReceiptLossRuntimeCookiePG(t *testing.T) {
	m8UntrackedOrganizationCommitReceiptLoss(t, "hardlink")
}

func TestM8UntrackedOrganizationCopyMoveCommitReceiptLossRuntimeCookiePG(t *testing.T) {
	for _, action := range []string{"copy", "move"} {
		t.Run(action, func(t *testing.T) { m8UntrackedOrganizationCommitReceiptLoss(t, action) })
	}
}

func m8UntrackedOrganizationCommitReceiptLoss(t *testing.T, action string) {
	t.Helper()
	m8UntrackedOrganizationRequired(t)
	f := m8UntrackedOrganizationNew(t, action)
	history := f.confirmationHistory(t)
	receipt, raw := f.queue(t)
	var dropped, probeErrors atomic.Int32
	f.configureRuntimePool = func(config *pgxpool.Config) {
		if config.ConnConfig.TLSConfig != nil {
			t.Fatal("owned PG transport fixture requires non-TLS local connection")
		}
		dial := config.ConnConfig.DialFunc
		config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dial(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return &m8CommitReceiptConn{Conn: conn, dropped: &dropped, committed: func() bool {
				probe, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Second)
				defer cancel()
				var committed bool
				err := f.pool.QueryRow(probe, `SELECT EXISTS(SELECT 1 FROM untracked_organization_completion c JOIN untracked_organization_operation o ON o.id=c.operation_id JOIN job j ON j.id=o.job_id WHERE o.id=$1 AND j.state='succeeded')`, receipt.Operation.ID).Scan(&committed)
				if err != nil {
					probeErrors.Add(1)
					return false
				}
				return committed
			}}, nil
		}
	}
	f.start(t)
	f.wait(t, "real final COMMIT response discarded after committed ledger", func() bool { return dropped.Load() == 1 })
	f.completed(t, receipt)
	f.stop(t)
	if probeErrors.Load() != 0 || dropped.Load() != 1 {
		t.Fatal("COMMIT fault did not match exactly one independently committed ledger")
	}
	f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='succeeded' AND attempts=1 AND lease_token=1 AND max_attempts=3`, 1, receipt.Operation.JobID)
	f.verifyImport(t, receipt, action)
	frozen := f.frozenTransfer(t, receipt)
	ledger := m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_organization_operation", "untracked_organization_unit", "untracked_organization_permit", "untracked_organization_observation", "untracked_organization_completion", "transfer_content_evidence", "library_file_unit", "unit_fact")
	f.start(t)
	restarted := time.Now()
	f.wait(t, "restart crosses recovery maintenance without replaying committed job", func() bool { return time.Since(restarted) >= 3500*time.Millisecond })
	f.completed(t, receipt)
	f.stop(t)
	if ledger != m8ConfigSnapshot(t, f.m7ScrapeFixture, "untracked_organization_operation", "untracked_organization_unit", "untracked_organization_permit", "untracked_organization_observation", "untracked_organization_completion", "transfer_content_evidence", "library_file_unit", "unit_fact") || frozen != f.frozenTransfer(t, receipt) {
		t.Fatal("commit receipt loss or restart changed original ledger")
	}
	f.count(t, `SELECT count(*) FROM job WHERE id=$1 AND state='succeeded' AND attempts=1 AND lease_token=1`, 1, receipt.Operation.JobID)
	f.verifyImport(t, receipt, action)
	f.recoverOffline(t, receipt, raw, "completed")
	if history != f.confirmationHistory(t) || dropped.Load() != 1 {
		t.Fatal("receipt recovery changed original confirmation or repeated fault")
	}
	t.Logf("M8_UNTRACKED_COMMIT_RECEIPT_PASS server_committed=true client_reply_discarded=true dropped_frames=1 original_ledger=true original_job_attempts=1 restart_no_reexecution=true offline_original_receipt=true downloader_calls=0 action=%s", action)
}
