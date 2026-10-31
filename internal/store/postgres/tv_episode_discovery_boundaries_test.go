package postgres_test

import (
 "encoding/json"
 "testing"
 "github.com/dalamudx/PulseMedia/internal/domain/quality"
 "github.com/dalamudx/PulseMedia/internal/platform/crypto"
 "github.com/dalamudx/PulseMedia/internal/platform/idgen"
 "github.com/dalamudx/PulseMedia/internal/ports"
 "github.com/dalamudx/PulseMedia/internal/store/postgres"
 "github.com/dalamudx/PulseMedia/internal/store/postgres/db"
 usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
 usecaseSubscription "github.com/dalamudx/PulseMedia/internal/usecase/subscription"
 "bytes"
 "github.com/google/uuid"
)

func TestTVEpisodeDiscoveryCutoffModesPG(t *testing.T){
 for _,mode:=range []string{"stop_at_cutoff","replace_if_better"}{t.Run(mode,func(t *testing.T){
  f,s,key:=tvEpisodeDiscoveryFixture(t);ctx:=t.Context()
  rules,_:=json.Marshal(quality.PolicyRules{CutoffScore:340,ResolutionOrder:map[quality.Resolution]int{quality.Res1080p:300,quality.Res2160p:400},SourceOrder:map[quality.Source]int{quality.SourceWEBDL:40}})
  if _,e:=db.New(f.pool).InsertPolicyRevision(ctx,db.InsertPolicyRevisionParams{ID:uuid.New(),PolicyID:"mp22",Revision:2,Rules:rules});e!=nil{t.Fatal(e)}
  execIntent(t,f.pool,`UPDATE policy SET current_revision=2 WHERE id='mp22'`)
  execIntent(t,f.pool,`UPDATE subscription SET quality_policy_revision=2,upgrade_mode=$2,intent_version=intent_version+1 WHERE id=$1`,f.sub,mode)
  if _,e:=usecaseSubscription.NewEvaluator(postgres.NewWantedProjectionStore(f.pool)).EvaluateWantedItem(ctx,f.wanted,quality.Policy{ID:"mp22",Revision:2});e!=nil{t.Fatal(e)}
  ok,e:=s.ScheduleTVEpisodeDiscovery(ctx,key);if e!=nil || ok!=(mode=="replace_if_better"){t.Fatalf("mode=%s scheduled=%v error=%v",mode,ok,e)}
 })}
}
func TestTVEpisodeDiscoverySharedAdmissionAndScopePG(t *testing.T){
 f,s,key:=tvEpisodeDiscoveryFixture(t);ctx:=t.Context()
 searches:=usecaseSearch.NewInteractiveSearchService(postgres.NewInteractiveSearchStore(f.pool,idgen.New()),crypto.NewSecretProtector(bytes.Repeat([]byte{27},32)))
 for i:=range 4{if _,e:=searches.Create(ctx,f.actor,ports.InteractiveSearchInput{Query:"ordinary shared budget"},"mp31-manual-cap-"+string(rune('a'+i)));e!=nil{t.Fatal("manual shared admission",e)}}
 if ok,e:=s.ScheduleTVEpisodeDiscovery(ctx,key);e!=nil || ok{t.Fatal("discovery bypassed shared cap",ok,e)}
 if n:=intentCount(t,f.pool,`SELECT count(*) FROM interactive_search WHERE state='running' AND deadline>clock_timestamp()`);n!=4{t.Fatal("shared search cap",n)}
 // Current administrator ownership is narrower than global administrative scope.
 other:=&ports.Actor{Type:ports.ActorTypeUser,ID:uuid.NewString(),Role:ports.RoleAdmin}
 if _,e:=db.New(f.pool).CreateUser(ctx,db.CreateUserParams{ID:uuid.MustParse(other.ID),Username:"mp31-other-admin",PasswordHash:"fixture",Role:ports.RoleAdmin,IsActive:true});e!=nil{t.Fatal(e)}
 if _,e:=s.ReadTVEpisodeDiscovery(ctx,other,key);e==nil{t.Fatal("foreign admin read original owner's discovery")}
 before:=intentCount(t,f.pool,`SELECT count(*) FROM subscription_tv_episode_discovery_run`)
 for range 3{status,e:=s.ReadTVEpisodeDiscovery(ctx,f.actor,key);if e!=nil || status.SearchID!=nil || status.ExecutionAllowed{t.Fatal("capacity waiting status",status,e)}}
 if after:=intentCount(t,f.pool,`SELECT count(*) FROM subscription_tv_episode_discovery_run`);after!=before{t.Fatal("GET created a run")}
}
