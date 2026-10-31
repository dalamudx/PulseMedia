package app

import (
 "context"
 "time"
 "github.com/dalamudx/PulseMedia/internal/store/postgres"
 usecaseSearch "github.com/dalamudx/PulseMedia/internal/usecase/search"
)

// runTVEpisodeDiscoveryBatch runs synchronously inside the existing App search
// scheduler. Its cancellation and shutdown remain owned by that lifecycle.
func(a *App) runTVEpisodeDiscoveryBatch(ctx context.Context){
 if a.dbPool==nil{return}
 bounded,cancel:=context.WithTimeout(ctx,5*time.Second);defer cancel()
 service:=usecaseSearch.NewTVEpisodeDiscoveryService(postgres.NewTVEpisodeDiscoveryStore(a.dbPool,a.idgen))
 if _,e:=service.RunBatch(bounded,20);e!=nil && ctx.Err()==nil{a.logger.Error("TV episode upgrade discovery scheduling deferred")}
}
