package search

import (
 "context"
 "errors"
 "testing"
 "github.com/dalamudx/PulseMedia/internal/ports"
 "github.com/google/uuid"
)
type tvEpisodeDiscoveryTestRepository struct{units []ports.TVEpisodeDiscoveryUnit;listed int32;scheduled,read int;fail error}
func(r *tvEpisodeDiscoveryTestRepository) ListDueTVEpisodeDiscoveries(_ context.Context,n int32)([]ports.TVEpisodeDiscoveryUnit,error){r.listed=n;return r.units,nil}
func(r *tvEpisodeDiscoveryTestRepository) ScheduleTVEpisodeDiscovery(_ context.Context,_ ports.TVEpisodeDiscoveryUnit)(bool,error){r.scheduled++;return r.fail==nil,r.fail}
func(r *tvEpisodeDiscoveryTestRepository) ReadTVEpisodeDiscovery(_ context.Context,_ *ports.Actor,key ports.TVEpisodeDiscoveryUnit)(*ports.TVEpisodeDiscoveryStatus,error){r.read++;return &ports.TVEpisodeDiscoveryStatus{SubscriptionID:key.SubscriptionID,UnitID:key.UnitID,State:"waiting"},nil}
func TestTVEpisodeDiscoveryBatchBoundAndCancellation(t *testing.T){
 repo:=&tvEpisodeDiscoveryTestRepository{units:make([]ports.TVEpisodeDiscoveryUnit,30)};service:=NewTVEpisodeDiscoveryService(repo)
 if n,e:=service.RunBatch(t.Context(),100);e!=nil || n!=20 || repo.listed!=20 || repo.scheduled!=20{t.Fatal(n,e,repo)}
 ctx,cancel:=context.WithCancel(t.Context());cancel();if _,e:=service.RunBatch(ctx,1);!errors.Is(e,context.Canceled) || repo.scheduled!=20{t.Fatal("cancel did not bound dispatch",e,repo)}
 repo.fail=ports.ErrRejected;repo.scheduled=0;if n,e:=service.RunBatch(t.Context(),2);e!=nil || n!=0 || repo.scheduled!=2{t.Fatal("ineligible does not starve batch",n,e,repo)}
}
func TestTVEpisodeDiscoveryReadAuthorizationAndNoScheduling(t *testing.T){
 repo:=&tvEpisodeDiscoveryTestRepository{};service:=NewTVEpisodeDiscoveryService(repo);sub,unit:=uuid.New(),uuid.New()
 for _,actor:=range []*ports.Actor{nil,{Type:ports.ActorTypeSystem,ID:uuid.NewString(),Role:ports.RoleAdmin},{Type:ports.ActorTypeUser,ID:uuid.NewString(),Role:ports.RoleMember},{Type:ports.ActorTypeUser,ID:uuid.NewString(),Role:ports.RoleAdmin,Scopes:[]string{"read"}}}{if _,e:=service.Read(t.Context(),actor,sub,unit);e==nil{t.Fatal("unauthorized status",actor)}}
 actor:=&ports.Actor{Type:ports.ActorTypeUser,ID:uuid.NewString(),Role:ports.RoleAdmin}
 status,e:=service.Read(t.Context(),actor,sub,unit);if e!=nil || status.SubscriptionID!=sub || status.UnitID!=unit || status.ExecutionAllowed || repo.read!=1 || repo.scheduled!=0 || repo.listed!=0{t.Fatal("read must not schedule",status,e,repo)}
 if _,e=service.Read(t.Context(),actor,sub,uuid.Nil);!errors.Is(e,ports.ErrRejected){t.Fatal("unit required",e)}
}
