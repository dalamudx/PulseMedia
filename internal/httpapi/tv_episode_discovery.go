package httpapi

import (
 "context"
 "net/http"
 "github.com/dalamudx/PulseMedia/internal/ports"
 usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

func(s *ServerHandler) SetTVEpisodeDiscoveryService(service *usecaseSearch.TVEpisodeDiscoveryService){s.tvEpisodeDiscoverySvc=service}
type tvEpisodeDiscoveryHTTPResponse struct{interactiveSearchHTTPResponse}
func(r tvEpisodeDiscoveryHTTPResponse) VisitGetTVEpisodeDiscoveryResponse(w http.ResponseWriter)error{return r.write(w)}
func(s *ServerHandler) GetTVEpisodeDiscovery(ctx context.Context,r GetTVEpisodeDiscoveryRequestObject)(GetTVEpisodeDiscoveryResponseObject,error){
 actor:=GetActor(ctx)
 if actor==nil{return tvEpisodeDiscoveryHTTPResponse{interactiveSearchFailure(ctx,ports.ErrUnauthorized)},nil}
 if s.tvEpisodeDiscoverySvc==nil{return tvEpisodeDiscoveryHTTPResponse{interactiveSearchFailure(ctx,ports.ErrUnavailable)},nil}
 status,e:=s.tvEpisodeDiscoverySvc.Read(ctx,actor,r.Id,r.UnitId)
 if e!=nil{return tvEpisodeDiscoveryHTTPResponse{interactiveSearchFailure(ctx,e)},nil}
 return tvEpisodeDiscoveryHTTPResponse{interactiveSearchHTTPResponse{status:http.StatusOK,body:status}},nil
}
