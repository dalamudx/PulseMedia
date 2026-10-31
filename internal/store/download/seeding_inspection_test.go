package download

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadcase "github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
)

type nilInspectionObserver struct{ ports.DownloadClient }

func (nilInspectionObserver) SeedingObserver() ports.SeedingReadClient { return nil }

type inspectionNativeProbe struct {
	ports.DownloadClient
	read  func(context.Context, string, string) (*ports.SeedingRemoteTorrent, error)
	calls atomic.Int32
}

func (p *inspectionNativeProbe) SeedingObserver() ports.SeedingReadClient { return p }
func (p *inspectionNativeProbe) ReadSeedingTorrent(ctx context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
	p.calls.Add(1)
	return p.read(ctx, hash, tag)
}
func (*inspectionNativeProbe) ReadSeedingFreeSpace(context.Context, string) (ports.SeedingPathSpace, error) {
	panic("inspection requested free space")
}
func newInspectionFixture(t *testing.T) (*controlFixture, *inspectionNativeProbe, *downloadcase.SeedingInspectionService) {
	t.Helper()
	f := newControlFixture(t)
	native := &inspectionNativeProbe{DownloadClient: f.client, read: func(_ context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
		return &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: f.task.SavePath, TotalBytes: 100}, nil
	}}
	f.repo.RegisterClient(f.task.ClientID, native)
	return f, native, downloadcase.NewSeedingInspectionService(f.repo, clock.New())
}
func assertInspectionUnchanged(t *testing.T, f *controlFixture, before map[string]string, task string) {
	t.Helper()
	if !reflect.DeepEqual(before, f.protected(t)) || task != f.taskJSON(t) {
		t.Fatal("inspection changed task or protected business state")
	}
	for _, table := range []string{"download_control_command", "download_control_return", "download_control_confirmation", "seeding_removal_permit", "download_rate_observation"} {
		if f.count(t, table) != 0 {
			t.Fatal("inspection persisted execution or telemetry", table)
		}
	}
}
func TestSeedingInspectionOwnerSharedAndCurrentRolePG(t *testing.T) {
	f, native, service := newInspectionFixture(t)
	other := uuid.New()
	ratePublisherExec(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role) VALUES($1,'inspection-other','unused','member')`, other)
	ratePublisherExec(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id)VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), f.task.OperationID, f.unit, f.root, other.String())
	before, task := f.protected(t), f.taskJSON(t)
	for _, actor := range []*ports.Actor{f.owner, {Type: ports.ActorTypeUser, ID: other.String(), Role: ports.RoleMember}} {
		result, err := service.Inspect(t.Context(), actor, f.task.OperationID)
		if err != nil || result == nil || result.ShareRatio != nil || result.SeedingSeconds != nil {
			t.Fatal("shared owner read rejected or unknown facts changed", result, err)
		}
	}
	spoof := *f.owner
	spoof.Role = ports.RoleAdmin
	if result, err := service.Inspect(t.Context(), &spoof, f.task.OperationID); result != nil || !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("role claim trusted", result, err)
	}
	outsider := uuid.New()
	ratePublisherExec(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role)VALUES($1,'inspection-outsider','unused','member')`, outsider)
	if result, err := service.Inspect(t.Context(), &ports.Actor{Type: ports.ActorTypeUser, ID: outsider.String(), Role: ports.RoleMember}, f.task.OperationID); result != nil || !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("unowned read", result, err)
	}
	ratePublisherExec(t, f.pool, `UPDATE app_user SET role='admin' WHERE id=$1`, outsider)
	if result, err := service.Inspect(t.Context(), &ports.Actor{Type: ports.ActorTypeUser, ID: outsider.String(), Role: ports.RoleAdmin}, f.task.OperationID); result == nil || err != nil {
		t.Fatal("current admin read", result, err)
	}
	if native.calls.Load() != 3 {
		t.Fatal("denial touched native", native.calls.Load())
	}
	assertInspectionUnchanged(t, f, before, task)
}
func TestSeedingInspectionConfigurationAndRoleChangeDiscardPG(t *testing.T) {
	for _, change := range []string{"configuration", "role", "requirements"} {
		t.Run(change, func(t *testing.T) {
			f, native, service := newInspectionFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			native.read = func(_ context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
				close(entered)
				<-release
				return &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: f.task.SavePath}, nil
			}
			done := make(chan error, 1)
			go func() {
				result, err := service.Inspect(t.Context(), f.owner, f.task.OperationID)
				if result != nil {
					err = errors.New("stale facts published")
				}
				done <- err
			}()
			var once sync.Once
			drain := func() {
				once.Do(func() {
					close(release)
					if err := <-done; err == nil {
						t.Error("changed authority published native facts")
					}
				})
			}
			defer drain()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("native read not started")
			}
			switch change {
			case "configuration":
				ratePublisherExec(t, f.pool, `UPDATE download_client SET url='https://changed.invalid',updated_at=clock_timestamp() WHERE id=$1`, f.task.ClientID)
			case "role":
				ratePublisherExec(t, f.pool, `UPDATE app_user SET role='admin' WHERE id=$1`, uuid.MustParse(f.owner.ID))
			case "requirements":
				ratePublisherExec(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id)VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), f.task.OperationID, f.unit, f.root, uuid.NewString())
			}
			before, task := f.protected(t), f.taskJSON(t)
			drain()
			assertInspectionUnchanged(t, f, before, task)
		})
	}
}
func TestSeedingInspectionCancellationRetainsPhysicalBudgetPG(t *testing.T) {
	f, native, service := newInspectionFixture(t)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	native.read = func(_ context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
		entered <- struct{}{}
		<-release
		return &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: f.task.SavePath}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 2)
	for range 2 {
		go func() {
			result, err := service.Inspect(ctx, f.owner, f.task.OperationID)
			if result != nil {
				err = errors.New("cancelled facts published")
			}
			done <- err
		}()
	}
	var once sync.Once
	drain := func() {
		once.Do(func() {
			close(release)
			for range 2 {
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Errorf("late native cancellation: %v", err)
				}
			}
		})
	}
	defer drain()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("native calls did not occupy physical budget")
		}
	}
	cancel()
	probe, stop := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer stop()
	if result, err := service.Inspect(probe, f.owner, f.task.OperationID); result != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancellation released physical budget", result, err)
	}
	if native.calls.Load() != 2 {
		t.Fatal("third native call admitted")
	}
	select {
	case <-done:
		t.Fatal("returned before physical read")
	default:
	}
	before, task := f.protected(t), f.taskJSON(t)
	drain()
	assertInspectionUnchanged(t, f, before, task)
	native.read = func(_ context.Context, hash, tag string) (*ports.SeedingRemoteTorrent, error) {
		return &ports.SeedingRemoteTorrent{InfoHash: hash, OperationTag: tag, SavePath: f.task.SavePath}, nil
	}
	if result, err := service.Inspect(t.Context(), f.owner, f.task.OperationID); result == nil || err != nil {
		t.Fatal("physical budget not released after return", result, err)
	}
}
func TestSeedingInspectionUnsupportedAndOriginalBindingPG(t *testing.T) {
	f, native, service := newInspectionFixture(t)
	f.repo.RegisterClient(f.task.ClientID, f.client)
	if result, err := service.Inspect(t.Context(), f.owner, f.task.OperationID); result != nil || !errors.Is(err, ports.ErrDownloadControlUnsupported) {
		t.Fatal("unsupported native read", result, err)
	}
	f.repo.RegisterClient(f.task.ClientID, nilInspectionObserver{f.client})
	if result, err := service.Inspect(t.Context(), f.owner, f.task.OperationID); result != nil || !errors.Is(err, ports.ErrDownloadControlUnsupported) {
		t.Fatal("nil native reader admitted", result, err)
	}
	f.repo.RegisterClient(f.task.ClientID, native)
	ratePublisherExec(t, f.pool, `UPDATE download_client SET username='changed' WHERE id=$1`, f.task.ClientID)
	if result, err := service.Inspect(t.Context(), f.owner, f.task.OperationID); result != nil || !errors.Is(err, ports.ErrDownloadControlConflict) || native.calls.Load() != 0 {
		t.Fatal("unbound endpoint read", result, err)
	}
}
