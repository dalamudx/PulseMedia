package postgres_test

import (
 "errors"
 "testing"

 "github.com/dalamudx/PulseMedia/internal/ports"
 "github.com/google/uuid"
)

func TestDedicatedDownloadControlCurrentSharedPermissionsPG(t *testing.T){
 for _,scope:=range dedicatedControlScopes{t.Run(scope,func(t *testing.T){
  f:=newDedicatedControl(t,scope)
  outsider,admin:=uuid.New(),uuid.New()
  execIntent(t,f.pool,`INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1::uuid,'control-outsider-'||$1::text,password_hash,'member',true FROM app_user WHERE id=$2`,outsider,uuid.MustParse(f.actor.ID))
  execIntent(t,f.pool,`INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1::uuid,'control-admin-'||$1::text,password_hash,'admin',true FROM app_user WHERE id=$2`,admin,uuid.MustParse(f.actor.ID))
  command:=ports.DownloadControlCommand{Action:"pause",Confirmed:true}
  other:=&ports.Actor{Type:ports.ActorTypeUser,ID:outsider.String(),Role:ports.RoleMember}
  if _,e:=f.service.Apply(t.Context(),other,f.task.OperationID,"outsider",command);!errors.Is(e,ports.ErrForbidden){t.Fatal("unrelated member gained task control",e)}
  token:=*f.actor;token.Scopes=[]string{"admin"}
  if _,e:=f.service.Apply(t.Context(),&token,f.task.OperationID,"scoped-token",command);!errors.Is(e,ports.ErrForbidden){t.Fatal("token scope gained business authority",e)}
  operator:=&ports.Actor{Type:ports.ActorTypeUser,ID:admin.String(),Role:ports.RoleAdmin}
  if _,e:=f.service.Apply(t.Context(),operator,f.task.OperationID,"unconfirmed-shared",command);!errors.Is(e,ports.ErrForbidden){t.Fatal("administrator omitted shared-impact confirmation",e)}
  if f.sends.Load()!=0||f.reads.Load()!=0{t.Fatal("failed command permission reached native downloader")}
  before:=f.protected(t)
  command.AffectShared=true
  out,e:=f.service.Apply(t.Context(),operator,f.task.OperationID,"confirmed-shared",command)
  if e!=nil||out==nil||out.State!="confirmed"||f.sends.Load()!=1{t.Fatal("explicit current admin shared permission",out,e)}
  if f.protected(t)!=before{t.Fatal("shared pause released original demands or reservations")}
  // A current account change invalidates receipt access too.
  execIntent(t,f.pool,`UPDATE app_user SET is_active=false WHERE id=$1`,admin)
  if _,e=f.service.Get(t.Context(),operator,f.task.OperationID,"confirmed-shared");!errors.Is(e,ports.ErrForbidden){t.Fatal("disabled administrator read protected receipt",e)}
 })}
}
