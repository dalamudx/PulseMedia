package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Uses the original native protocol, Cookie, sparse Wanted and real FD import
// fixture. Only the close_write hint is new; no observation clock is rewritten.
func TestM8AutomaticTVDirectoryHintHTTPPG(t *testing.T) {
	if os.Getenv("PULSE_M8_AUTOMATIC_TV_WATCH_ACCEPTANCE") != "1" {
		t.Skip("set PULSE_M8_AUTOMATIC_TV_WATCH_ACCEPTANCE=1 with an owned PG18.6 fixture")
	}
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux inotify")
	}
	t.Setenv("PULSE_M5_AUTOMATIC_TV_ACCEPTANCE", "1")
	for _, lostACK := range []bool{false, true} {
		name := "ack"
		if lostACK {
			name = "lostACK"
		}
		t.Run(name, func(t *testing.T) {
			h := newM5AutomaticTVAcceptance(t, lostACK)
			receipt := h.create(t)
			op := h.awaitAdd(t)
			h.unblockAdd()

			var prior, task, send uuid.UUID
			var priorJSON []byte
			var created, nextPoll, bootFence time.Time
			m5AutomaticTVWait(t, 60*time.Second, "genuine successful partial observation", func(ctx context.Context) (bool, error) {
				err := h.f.app.DBPool().QueryRow(ctx, `SELECT r.job_id,r.task_id,r.send_attempt_id,to_jsonb(r),r.created_at,r.next_poll_at
 FROM automatic_tv_observation_result r JOIN job j ON j.id=r.job_id
 WHERE r.operation_id=$1 AND r.state='tracking' AND NOT r.complete AND NOT r.review_required AND j.state='succeeded'
 ORDER BY r.created_at DESC,r.job_id DESC LIMIT 1`, op).Scan(&prior, &task, &send, &priorJSON, &created, &nextPoll)
				if errors.Is(err, pgx.ErrNoRows) {
					return false, nil
				}
				if err != nil {
					return false, errors.New("partial observation read failed")
				}
				return true, nil
			})
			if nextPoll.Sub(created) < 2*time.Second || h.complete.Load() {
				t.Fatal("partial observation did not preserve the two-second gate")
			}
			if lostACK && h.count(t, `SELECT count(*) FROM automatic_tv_submission_result WHERE operation_id=$1 AND state='submission_unknown'`, op) != 1 {
				t.Fatal("lost ACK did not preserve original unknown submission")
			}
			metadataCalls := h.metadata.count()
			identity := m8AutomaticTVWatchIdentity(t, h, op)
			jobs := h.count(t, `SELECT count(*) FROM automatic_tv_observation_job WHERE operation_id=$1`, op)
			permits := h.count(t, `SELECT count(*) FROM automatic_tv_observation_permit WHERE operation_id=$1`, op)
			if h.f.stopRuntime == nil || h.f.newRuntime == nil {
				t.Fatal("creation fixture runtime hooks missing")
			}
			if err := h.f.app.DBPool().QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&bootFence); err != nil {
				t.Fatal("boot fence read failed")
			}
			h.f.stopRuntime()
			a, err := h.f.newRuntime()
			if err != nil {
				t.Fatal("replacement App construction failed")
			}
			// The old App is fully joined before replacing the pool used by every
			// inherited assertion. Cleanup is registered after all fixture servers.
			h.f.app = a
			done := m8AutomaticTVWatchStart(t, a)
			client := &http.Client{Timeout: 200 * time.Millisecond}
			t.Cleanup(client.CloseIdleConnections)
			healthy, schedulerReady, kernelReady := false, false, false
			m5AutomaticTVWait(t, 2*time.Second, "restart before original due gate", func(ctx context.Context) (bool, error) {
				select {
				case <-done:
					return false, errors.New("replacement App exited during readiness")
				default:
				}
				if !healthy {
					req, e := http.NewRequestWithContext(ctx, http.MethodGet, h.f.base+"/api/v1/auth/status", nil)
					if e != nil {
						return false, errors.New("health request construction failed")
					}
					r, e := client.Do(req)
					if e == nil {
						healthy = r.StatusCode == http.StatusOK
						_ = r.Body.Close()
					}
				}
				if !healthy {
					return false, nil
				}
				schedulerReady = m8AutomaticTVWatchSchedulerWaiting()
				kernelReady = completeTVWatchKernelReady(t, filepath.Join(h.rootPath, m5AutomaticTVRelease))
				var beforeDue bool
				if e := a.DBPool().QueryRow(ctx, `SELECT clock_timestamp()<$1`, nextPoll).Scan(&beforeDue); e != nil {
					return false, errors.New("readiness clock read failed")
				}
				if !beforeDue {
					t.Fatalf("restart phase missed original gate: healthy=%t scheduler_waiting=%t kernel_ready=%t", healthy, schedulerReady, kernelReady)
				}
				return healthy && schedulerReady && kernelReady, nil
			})

			var unchanged, beforeDue, latest, noOtherJobs, noOtherPermits, detached bool
			if err = a.DBPool().QueryRow(t.Context(), `SELECT
 (SELECT to_jsonb(r)=$3::jsonb FROM automatic_tv_observation_result r WHERE r.job_id=$2),
 clock_timestamp()<$4,
 (SELECT job_id=$2 FROM automatic_tv_observation_result WHERE operation_id=$1 ORDER BY created_at DESC,job_id DESC LIMIT 1),
 (SELECT count(*)=$5 FROM automatic_tv_observation_job WHERE operation_id=$1),
 (SELECT count(*)=$6 FROM automatic_tv_observation_permit WHERE operation_id=$1),
 NOT EXISTS(SELECT 1 FROM automatic_tv_observation_permit WHERE operation_id=$1 AND finished_at IS NULL)`, op, prior, string(priorJSON), nextPoll, jobs, permits).Scan(&unchanged, &beforeDue, &latest, &noOtherJobs, &noOtherPermits, &detached); err != nil {
				t.Fatal("pre-event phase read failed")
			}
			if !unchanged || !beforeDue || !latest || !noOtherJobs || !noOtherPermits || !detached {
				t.Fatalf("pre-event phase: unchanged=%t before_due=%t latest=%t jobs_unchanged=%t permits_unchanged=%t detached=%t", unchanged, beforeDue, latest, noOtherJobs, noOtherPermits, detached)
			}
			eventStart := time.Now()
			h.complete.Store(true)
			if err = os.WriteFile(filepath.Join(h.rootPath, m5AutomaticTVRelease, h.names[0]), h.payloads[0], 0600); err != nil {
				t.Fatal("native close_write failed")
			}
			h.awaitImported(t, op, 60*time.Second)
			elapsed := time.Since(eventStart)

			var successor uuid.UUID
			var payload []byte
			var duePreserved, phaseExcluded, bindingPreserved bool
			if err = a.DBPool().QueryRow(t.Context(), `SELECT j.id,j.payload,j.created_at>=$3::timestamptz,j.created_at<$4::timestamptz+interval '3 seconds',
 b.task_id=$5 AND b.send_attempt_id=$6 AND r.task_id=$5 AND r.send_attempt_id=$6
 FROM automatic_tv_observation_result r JOIN job j ON j.id=r.job_id JOIN automatic_tv_observation_job b ON b.job_id=j.id
 WHERE r.operation_id=$1 AND r.job_id<>$2 AND r.complete AND NOT r.review_required AND r.state='completed'
 AND j.kind=$7 AND j.state='succeeded'`, op, prior, nextPoll, bootFence, task, send, ports.AutomaticTVObservationJobKind).Scan(&successor, &payload, &duePreserved, &phaseExcluded, &bindingPreserved); err != nil {
				t.Fatal("completed successor read failed")
			}
			if !duePreserved || !phaseExcluded || !bindingPreserved || successor == prior {
				t.Fatalf("successor phase: due_preserved=%t periodic_excluded=%t binding_preserved=%t", duePreserved, phaseExcluded, bindingPreserved)
			}
			var decoded map[string]uuid.UUID
			if json.Unmarshal(payload, &decoded) != nil || len(decoded) != 3 || decoded["operation_id"] != op || decoded["task_id"] != task || decoded["automatic_tv_authority"] != op {
				t.Fatal("successor changed the original three-key observation payload")
			}
			var finalPrior []byte
			if err = a.DBPool().QueryRow(t.Context(), `SELECT to_jsonb(r) FROM automatic_tv_observation_result r WHERE r.job_id=$1`, prior).Scan(&finalPrior); err != nil {
				t.Fatal("original observation reread failed")
			}
			if !bytes.Equal(priorJSON, finalPrior) || !bytes.Equal(identity, m8AutomaticTVWatchIdentity(t, h, op)) {
				t.Fatal("original observation or authority/preparation/send/task identity changed")
			}
			if h.count(t, `SELECT count(*) FROM automatic_tv_observation_job WHERE operation_id=$1`, op) != jobs+1 || h.count(t, `SELECT count(*) FROM automatic_tv_observation_permit WHERE operation_id=$1`, op) != permits+1 || h.count(t, `SELECT count(*) FROM download_task WHERE operation_id=$1 AND id=$2`, op, task) != 1 {
				t.Fatal("event must add exactly one normal observation job and permit for the original task")
			}
			if h.metadata.count() != metadataCalls {
				t.Fatal("directory hint performed additional metadata requests")
			}
			h.assertImported(t, op, receipt)
			t.Logf("M8_AUTOMATIC_TV_WATCH_APP_PASS phase_excluded=true two_second_gate_preserved=true Add=1 imported=1 inode_same=true elapsed_ms=%d", elapsed.Milliseconds())
		})
	}
}

func m8AutomaticTVWatchIdentity(t *testing.T, h *m5AutomaticTVAcceptance, op uuid.UUID) []byte {
	t.Helper()
	var identity []byte
	if err := h.f.app.DBPool().QueryRow(t.Context(), `SELECT jsonb_build_object('authority',to_jsonb(a),'preparation',to_jsonb(p),'send_attempt',to_jsonb(x),'task_id',t.id)
 FROM automatic_tv_execution_authority a JOIN automatic_tv_submission_preparation p USING(operation_id)
 JOIN automatic_tv_physical_attempt x ON x.id=p.physical_attempt_id JOIN download_task t ON t.id=p.task_id AND t.operation_id=p.operation_id
 WHERE a.operation_id=$1`, op).Scan(&identity); err != nil {
		t.Fatal("original identity read failed")
	}
	return identity
}

func m8AutomaticTVWatchStart(t *testing.T, a *app.App) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
				if runErr != nil {
					t.Error("replacement App run failed")
				}
			case <-time.After(50 * time.Second):
				t.Error("replacement App cancellation/join exceeded 50-second budget")
				return
			}
			if err := a.Close(); err != nil {
				t.Error("replacement App close failed")
			}
		})
	})
	go func() {
		defer close(done)
		runErr = a.Run(ctx)
	}()
	return done
}

// A scheduler suspended in its own select has completed its first maintenance
// pass. Merely seeing HTTP health or inotify registration cannot prove that.
// Inspect only the frame/state; never print goroutine stacks or arguments.
func m8AutomaticTVWatchSchedulerWaiting() bool {
	stack := make([]byte, 2<<20)
	n := runtime.Stack(stack, true)
	if n == len(stack) {
		return false
	}
	for _, block := range strings.Split(string(stack[:n]), "\n\n") {
		lines := strings.SplitN(block, "\n", 3)
		if len(lines) >= 2 && strings.Contains(lines[0], "[select]") && strings.HasPrefix(lines[1], "github.com/dalamudx/PulseMedia/internal/app.(*App).runAutomaticTVObservationWorkers(") {
			return true
		}
	}
	return false
}
