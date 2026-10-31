package postgres_test

import (
 "context"
 "errors"
 "strings"
 "sync"
 "testing"
 "time"

 "github.com/google/uuid"
 "github.com/jackc/pgx/v5"
 "github.com/jackc/pgx/v5/pgconn"
)

func TestDedicatedDownloadControlDMLRoleTamperPG(t *testing.T){
 for _,scope:=range dedicatedControlScopes{t.Run(scope,func(t *testing.T){
  f:=newDedicatedControl(t,scope)
  role:="control_dml_"+strings.ReplaceAll(uuid.NewString(),"-","")
  quoted:=pgx.Identifier{role}.Sanitize()
  execIntent(t,f.pool,"CREATE ROLE "+quoted)
  t.Cleanup(func(){
   ctx,cancel:=context.WithTimeout(context.Background(),5*time.Second);defer cancel()
   if _,e:=f.pool.Exec(ctx,"DROP OWNED BY "+quoted);e!=nil{t.Error(e)}
   if _,e:=f.pool.Exec(ctx,"DROP ROLE "+quoted);e!=nil{t.Error(e)}
  })
  execIntent(t,f.pool,"GRANT USAGE ON SCHEMA public TO "+quoted)
  execIntent(t,f.pool,"GRANT SELECT,INSERT,UPDATE,DELETE,TRUNCATE ON ALL TABLES IN SCHEMA public TO "+quoted)
  entered,release:=make(chan struct{}),make(chan struct{})
  hook:=func(){close(entered);<-release};f.onSend.Store(&hook)
  done:=make(chan error,1)
  go func(){_,e:=f.apply(t.Context(),"dml-owned-call","pause");done<-e}()
  var once sync.Once
  drain:=func(){once.Do(func(){close(release);if e:=<-done;e!=nil{t.Error(e)}})}
  defer drain()
  select{case <-entered:case <-time.After(10*time.Second):t.Fatal("native call not admitted")}
  for _,query:=range []string{
   `UPDATE download_control_authority SET digest=repeat('f',64)`,
   `DELETE FROM download_control_authority`,
   `UPDATE download_control_physical SET backend_pid=pg_backend_pid()`,
   `DELETE FROM download_control_physical`,
   `TRUNCATE download_control_authority`,
   `TRUNCATE download_control_physical`,
   `INSERT INTO download_control_return(command_id) SELECT id FROM download_control_command`,
  }{
   tx,e:=f.pool.Begin(t.Context());if e!=nil{t.Fatal(e)}
   if _,e=tx.Exec(t.Context(),"SET LOCAL ROLE "+quoted);e!=nil{_=tx.Rollback(t.Context());t.Fatal(e)}
   _,e=tx.Exec(t.Context(),query)
   _=tx.Rollback(t.Context())
   var pg *pgconn.PgError
   if !errors.As(e,&pg)||pg.Code!="23514"{t.Fatalf("DML tamper must hit immutable/authentic-return constraint: %s: %v",query,e)}
  }
  if n:=intentCount(t,f.pool,`SELECT count(*) FROM download_control_return`);n!=0{t.Fatal("another backend forged native return")}
  drain()
  if n:=intentCount(t,f.pool,`SELECT count(*) FROM download_control_return`);n!=1{t.Fatal("original native return missing")}
  if n:=intentCount(t,f.pool,`SELECT count(*) FROM download_control_physical`);n!=0{t.Fatal("authentic native return did not release hold")}
 })}
}
