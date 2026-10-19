package postgres_test

import (
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

func TestDashboardActivityAutomaticOwnerIdentityPG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	f.claim(t)
	snapshot := f.load(t)
	if receipt, err := f.observer.CommitAutomaticTVObservation(t.Context(), *snapshot, automaticTVObservationRemote(snapshot, false)); err != nil || receipt.State != "tracking" || receipt.ReviewRequired {
		t.Fatalf("real partial observation: %+v %v", receipt, err)
	}
	other := ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	admin := ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	for _, actor := range []ports.Actor{other, admin} {
		execIntent(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,$2,'fixture',$3,true)`, uuid.MustParse(actor.ID), actor.ID, actor.Role)
	}
	var role string
	if err := f.pool.QueryRow(t.Context(), `SELECT role FROM app_user WHERE id=$1`, uuid.MustParse(f.actor.ID)).Scan(&role); err != nil || role != ports.RoleMember {
		t.Fatalf("automatic owner must be a real member: %q %v", role, err)
	}
	ownerRead, ownerEmpty, ownerWrite, ownerAdmin := *f.actor, *f.actor, *f.actor, *f.actor
	ownerRead.Scopes = []string{"read"}
	ownerEmpty.Scopes = []string{}
	ownerWrite.Scopes = []string{"write"}
	ownerAdmin.Role = ports.RoleAdmin
	for _, tc := range []struct {
		name        string
		actor       ports.Actor
		currentRole string
		active      bool
		visible     bool
		forbidden   bool
	}{
		{"member_owner", *f.actor, ports.RoleMember, true, true, false},
		{"member_owner_read_scope", ownerRead, ports.RoleMember, true, true, false},
		{"other_member", other, ports.RoleMember, true, false, false},
		{"current_admin", admin, ports.RoleAdmin, true, true, false},
		{"empty_scope", ownerEmpty, ports.RoleMember, true, false, true},
		{"write_scope", ownerWrite, ports.RoleMember, true, false, true},
		{"claimed_admin", ownerAdmin, ports.RoleMember, true, false, true},
		{"disabled_owner", *f.actor, ports.RoleMember, false, false, true},
		{"owner_current_role_changed", *f.actor, ports.RoleAdmin, true, false, true},
		{"demoted_admin", admin, ports.RoleMember, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execIntent(t, f.pool, `UPDATE app_user SET role=$2,is_active=$3 WHERE id=$1`, uuid.MustParse(tc.actor.ID), tc.currentRole, tc.active)
			before := dashboardActivityGraph(t, f.pool)
			page, err := postgres.NewDashboardActivityStore(f.pool).ReadDashboardActivity(t.Context(), &tc.actor, 6)
			if tc.forbidden {
				if page != nil || !errors.Is(err, ports.ErrForbidden) {
					t.Fatalf("current identity must be refused: %+v %v", page, err)
				}
			} else {
				if err != nil || page == nil || page.Items == nil || page.More || page.ObservedAt.IsZero() {
					t.Fatalf("activity page: %+v %v", page, err)
				}
				if tc.visible {
					if len(page.Items) != 1 {
						t.Fatalf("owner/admin must see genuine automatic operation: %+v", page)
					}
					item := page.Items[0]
					if item.OperationID != f.request.OperationID || item.Status != "tracking" || item.Progress == nil || *item.Progress != 0.5 || item.ProgressSource != "verified_bytes" || item.UpdatedAt == nil {
						t.Fatalf("visible real verified observation: %+v", item)
					}
				} else if len(page.Items) != 0 {
					t.Fatalf("other member saw automatic owner's operation: %+v", page)
				}
			}
			assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
		})
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND actor_type='user'`, f.request.OperationID); n != 0 {
		t.Fatalf("automatic fixture acquired fabricated user demands: %d", n)
	}
}

func TestDashboardActivityAutomaticOwnerQueuedPG(t *testing.T) {
	f := automaticTVSubmissionBase(t, false)
	if version, err := postgres.Migrate(t.Context(), f.pool.Config().ConnString(), automaticTVObservationMigrations(t)); err != nil || version != 90 {
		t.Fatalf("observation tables for read-only graph: %d %v", version, err)
	}
	round := f.start(t)
	tvExecutionPage(t, f, round, tvExecutionCandidate(t, f, "dashboard-owner-queued", "1080p", 5))
	lease := tvExecutionLease(t, f, round)
	decision, err := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), round.ID, lease)
	if err != nil || decision.State != "reserved" {
		t.Fatalf("real automatic finalization: %+v %v", decision, err)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_execution_authority WHERE operation_id=$1 AND owner_id=$2 AND unit_id=$3 AND root_id=$4`, decision.OperationID, uuid.MustParse(f.actor.ID), f.unit, f.tv); n != 1 {
		t.Fatalf("matching real automatic authority: %d", n)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='automatic_tv_download_submission' AND state='queued' AND dedupe_key=$1`, decision.OperationID.String()); n != 1 {
		t.Fatalf("actual queued submission dedupe: %d", n)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task WHERE operation_id=$1`, decision.OperationID); n != 0 {
		t.Fatalf("queued test must precede download_task: %d", n)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_requirement WHERE operation_id=$1 AND actor_type='user'`, decision.OperationID); n != 0 {
		t.Fatalf("automatic operation must have no user demand: %d", n)
	}
	before := dashboardActivityGraph(t, f.pool)
	page := dashboardActivityRead(t, f.pool, f.actor, 1)
	if len(page.Items) != 1 || page.More || page.Items[0].OperationID != decision.OperationID || page.Items[0].Title != "Trusted Native Series" || page.Items[0].Status != "queued" || page.Items[0].Progress != nil || page.Items[0].ProgressSource != "" || page.Items[0].UpdatedAt != nil {
		t.Fatalf("automatic submission before task must be visible and queued: %+v", page)
	}
	legacy, err := postgres.NewOperationQueryStore(f.pool).ListOperations(t.Context(), f.actor, ports.OperationListRequest{View: "active", Limit: 6})
	if err != nil || legacy == nil || len(legacy.Items) != 0 {
		t.Fatalf("legacy operation API visibility changed: %+v %v", legacy, err)
	}
	assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
}

func TestDashboardActivityAutomaticOwnerMixedBoundsPG(t *testing.T) {
	f := newAutomaticTVObservationFixture(t, true)
	other := ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	admin := ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	for _, actor := range []ports.Actor{other, admin} {
		execIntent(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,$2,'fixture',$3,true)`, uuid.MustParse(actor.ID), actor.ID, actor.Role)
	}
	var at time.Time
	var candidate uuid.UUID
	if err := f.pool.QueryRow(t.Context(), `SELECT r.created_at,t.candidate_id FROM download_requirement r JOIN download_task t USING(operation_id) WHERE r.operation_id=$1`, f.request.OperationID).Scan(&at, &candidate); err != nil {
		t.Fatal(err)
	}
	ordinary := dashboardActivityFixture{creationFixture: f.creationFixture, unit: f.unit, candidate: candidate, client: f.client}
	ordinary.movie = f.tv
	first := ordinary.add(t, f.actor.ID, "tracking", at)
	second := ordinary.add(t, f.actor.ID, "tracking", at)
	older := ordinary.add(t, f.actor.ID, "tracking", at.Add(-time.Hour))
	foreign := ordinary.add(t, other.ID, "tracking", at.Add(time.Hour))
	// A shared ordinary operation must occupy one slot even for an administrator.
	execIntent(t, f.pool, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,created_at) VALUES($1,$2,$3,$4,'user',$5,$6)`, uuid.New(), first, f.unit, f.tv, other.ID, at)
	own := []uuid.UUID{f.request.OperationID, first, second}
	sort.Slice(own, func(i, j int) bool { return own[i].String() > own[j].String() })
	own = append(own, older)
	all := append([]uuid.UUID{foreign}, own...)
	before := dashboardActivityGraph(t, f.pool)
	for _, tc := range []struct {
		name  string
		actor *ports.Actor
		limit int
		want  []uuid.UUID
		more  bool
	}{
		{"owner_limit_one", f.actor, 1, own[:1], true},
		{"owner_tied_boundary", f.actor, 2, own[:2], true},
		{"owner_exact_bound", f.actor, 4, own, false},
		{"owner_default", f.actor, 0, own, false},
		{"other_member_own_scope", &other, 6, []uuid.UUID{foreign, first}, false},
		{"admin_all_deduplicated", &admin, 12, all, false},
		{"admin_limit_one", &admin, 1, all[:1], true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := dashboardActivityRead(t, f.pool, tc.actor, tc.limit)
			got := make([]uuid.UUID, 0, len(page.Items))
			for _, item := range page.Items {
				got = append(got, item.OperationID)
			}
			if !reflect.DeepEqual(got, tc.want) || page.More != tc.more || page.ObservedAt.IsZero() {
				t.Fatalf("combined scope/order/bounds: got %v more=%v, want %v more=%v", got, page.More, tc.want, tc.more)
			}
			if again := dashboardActivityRead(t, f.pool, tc.actor, tc.limit); !reflect.DeepEqual(page.Items, again.Items) || page.More != again.More {
				t.Fatal("combined operation order is unstable")
			}
		})
	}
	assertEpisodeRecheckSnapshot(t, before, dashboardActivityGraph(t, f.pool))
}
