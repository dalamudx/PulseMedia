package httpapi_test

import (
	"context"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

func TestSubscriptionListNativeTMDBNamespacePG(t *testing.T) {
	a, adminSession, _, movieRoot, _, _, _, _ := setupOperationHTTP(t)
	ctx := context.Background()
	pool := a.DBPool()
	admin, err := a.Queries().GetSessionByTokenHash(ctx, crypto.HashToken(adminSession))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	movieID, tvID, tvRoot := uuid.New(), uuid.New(), uuid.New()
	exec("INSERT INTO media(id,title,media_type) VALUES($1,'Native Movie','movie'),($2,'Native TV','tv')", movieID, tvID)
	exec("INSERT INTO library_root(id,path,media_type,name,baseline_completed) VALUES($1,$2,'tv','Native TV',true)", tvRoot, t.TempDir())
	for _, pair := range [][2]uuid.UUID{{movieID, movieRoot}, {tvID, tvRoot}} {
		exec("INSERT INTO subscription(id,user_id,media_id,root_id,quality_policy_id,quality_policy_revision) VALUES($1,$2,$3,$4,'default',1)", uuid.New(), admin.UserID, pair[0], pair[1])
	}
	identity := func(media uuid.UUID, namespace, external string) {
		t.Helper()
		exec("INSERT INTO media_identity(id,media_id,namespace,external_id) VALUES($1,$2,$3,$4)", uuid.New(), media, namespace, external)
	}
	identity(movieID, "tmdb.movie", "550")
	identity(tvID, "tmdb.tv", "550")
	identity(tvID, "tmdb.movie", "900")
	identity(tvID, "tmdb.tv.legacy", "901")
	reader := postgres.NewSubscriptionListStore(pool)
	actor := &ports.Actor{ID: admin.UserID.String(), Type: ports.ActorTypeUser, Role: "admin"}
	assertIdentities := func(tv string) {
		t.Helper()
		rows, err := reader.ReadSubscriptionList(ctx, actor, ports.SubscriptionListQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[uuid.UUID]string{}
		for _, row := range rows.Items {
			seen[row.MediaID] = row.TMDBExternalID
		}
		if seen[movieID] != "550" || seen[tvID] != tv {
			t.Fatalf("native namespace mismatch: movie=%q tv=%q", seen[movieID], seen[tvID])
		}
	}
	assertIdentities("550")
	identity(tvID, "tmdb.tv", "551")
	assertIdentities("")
}
