package search

import (
 "context"
 "errors"
 "github.com/dalamudx/PulseMedia/internal/ports"
 "github.com/google/uuid"
)

type TVEpisodeDiscoveryService struct { repo ports.TVEpisodeDiscoveryRepository }
func NewTVEpisodeDiscoveryService(repo ports.TVEpisodeDiscoveryRepository) *TVEpisodeDiscoveryService { return &TVEpisodeDiscoveryService{repo} }
// RunBatch creates observation jobs only; the App owns the cadence and lifecycle.
func(s *TVEpisodeDiscoveryService) RunBatch(ctx context.Context,limit int32)(int,error){
 if s==nil || s.repo==nil{return 0,ports.ErrUnavailable}
 if limit<1 || limit>20{limit=20}
 units,e:=s.repo.ListDueTVEpisodeDiscoveries(ctx,limit);if e!=nil{return 0,e}
 if len(units)>int(limit){units=units[:limit]}
 n:=0
 for _,u:=range units{
  if e:=ctx.Err();e!=nil{return n,e}
  ok,e:=s.repo.ScheduleTVEpisodeDiscovery(ctx,u)
  if errors.Is(e,ports.ErrRejected)||errors.Is(e,ports.ErrForbidden)||errors.Is(e,ports.ErrNotFound){continue}
  if e!=nil{return n,e};if ok{n++}
 }
 return n,nil
}
func(s *TVEpisodeDiscoveryService) Read(ctx context.Context,a *ports.Actor,subscriptionID,unitID uuid.UUID)(*ports.TVEpisodeDiscoveryStatus,error){
 if e:=authorizeInteractiveSearch(a);e!=nil{return nil,e}
 if a.Role!=ports.RoleAdmin{return nil,ports.ErrForbidden}
 if subscriptionID==uuid.Nil || unitID==uuid.Nil{return nil,ports.ErrRejected}
 if s==nil || s.repo==nil{return nil,ports.ErrUnavailable}
 return s.repo.ReadTVEpisodeDiscovery(ctx,a,ports.TVEpisodeDiscoveryUnit{SubscriptionID:subscriptionID,UnitID:unitID})
}
