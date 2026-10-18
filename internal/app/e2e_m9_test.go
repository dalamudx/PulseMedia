package app_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/dalamudx/PulseMedia/internal/domain/naming"
	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
)

func TestFullM9AdvancedWorkflow(t *testing.T) {
	cluster := pgtest.GetCluster(t)
	dbDSN := cluster.NewTestDB(t)

	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, dbDSN, migrationsDir); err != nil {
		t.Fatalf("failed to migrate db: %v", err)
	}

	pool, err := postgres.NewPool(ctx, dbDSN, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("failed to connect pool: %v", err)
	}
	defer pool.Close()

	queries := postgres.Queries(pool)

	// 1. M9-01: Downloader Automatic Routing by Media Type
	cMovieID, _ := uuid.NewV7()
	_, _ = queries.CreateDownloadClient(ctx, db.CreateDownloadClientParams{
		ID:                cMovieID,
		Name:              "qBittorrent Movie Dedicated",
		ClientType:        "qbittorrent",
		Url:               "http://qb-movie:8080",
		Username:          "admin",
		PasswordEncrypted: []byte("enc"),
		RemotePathMap:     []byte("{}"),
	})

	cTVID, _ := uuid.NewV7()
	_, _ = queries.CreateDownloadClient(ctx, db.CreateDownloadClientParams{
		ID:                cTVID,
		Name:              "Transmission TV Dedicated",
		ClientType:        "transmission",
		Url:               "http://tr-tv:9091",
		Username:          "admin",
		PasswordEncrypted: []byte("enc"),
		RemotePathMap:     []byte("{}"),
	})

	router := downloadstore.NewRouter(queries)
	selectedMovie, err := router.SelectClient(ctx, "movie", nil)
	if err != nil || selectedMovie.ID != cMovieID {
		t.Fatalf("expected movie client routed: %+v", selectedMovie)
	}
	selectedTV, err := router.SelectClient(ctx, "tv", nil)
	if err != nil || selectedTV.ID != cTVID {
		t.Fatalf("expected tv client routed: %+v", selectedTV)
	}

	// 2. M9-03: Custom Word Rules Scoring & Negative Filters
	customRules := []quality.CustomWordRule{
		{Word: "Atmos", Score: 100},
		{Word: "Dolby Vision", Score: 200},
		{Word: "TS", Excluded: true},
	}
	evalGood := quality.EvaluateCustomWords("Fight.Club.1999.Atmos.Dolby Vision.mkv", customRules)
	if !evalGood.Passed || evalGood.BonusScore != 300 {
		t.Fatalf("custom word scoring failed: %+v", evalGood)
	}
	evalBad := quality.EvaluateCustomWords("Fight.Club.1999.TS.mkv", customRules)
	if evalBad.Passed {
		t.Fatal("expected TS excluded")
	}

	// 3. M9-04: Naming Template Engine
	vars := map[string]string{
		"title":      "Fight Club",
		"year":       "1999",
		"resolution": "1080p",
		"source":     "Bluray",
	}
	formatted, err := naming.FormatNamingTemplate("{title} ({year})/{title} ({year}) [{resolution} {source}].mkv", vars)
	if err != nil || formatted != "Fight Club (1999)/Fight Club (1999) [1080p Bluray].mkv" {
		t.Fatalf("template rendering failed: %s, %v", formatted, err)
	}

	// 4. M9-05: Family Member Media Request & Approval Workflow
	adminID, _ := uuid.NewV7()
	_, _ = queries.CreateUser(ctx, db.CreateUserParams{ID: adminID, Username: "admin", PasswordHash: "h", Role: ports.RoleAdmin, IsActive: true})
	adminActor := &ports.Actor{Type: ports.ActorTypeUser, ID: adminID.String(), Name: "admin", Role: ports.RoleAdmin}

	memberID, _ := uuid.NewV7()
	_, _ = queries.CreateUser(ctx, db.CreateUserParams{ID: memberID, Username: "family_member", PasswordHash: "h", Role: ports.RoleMember, IsActive: true})
	memberActor := &ports.Actor{Type: ports.ActorTypeUser, ID: memberID.String(), Name: "family_member", Role: ports.RoleMember}

	rootID, _ := uuid.NewV7()
	_, _ = queries.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: rootID, Path: "/data/movies", MediaType: "movie", Name: "Movies"})
	_, _ = pool.Exec(ctx, "UPDATE library_root SET baseline_completed = true WHERE id = $1", rootID)

	mediaID, _ := uuid.NewV7()
	_, _ = queries.CreateMedia(ctx, db.CreateMediaParams{ID: mediaID, Title: "Gladiator", MediaType: "movie"})
	unitID, _ := uuid.NewV7()
	_, _ = queries.CreateMediaUnit(ctx, db.CreateMediaUnitParams{ID: unitID, MediaID: mediaID, UnitKey: "movie"})

	// Member submits subscription request
	gen := idgen.New()
	subSvcStore := postgres.NewSubscriptionIntentStore(pool, gen)
	subSvc := subscription.NewService(subSvcStore, subSvcStore, postgres.NewSubscriptionControlStore(pool, gen), subSvcStore)

	sub, _, err := subSvc.CreateSubscription(ctx, memberActor, subscription.CreateSubscriptionCommand{
		MediaID:   mediaID,
		RootID:    rootID,
		ScopeType: "all",
	})
	if err != nil || sub.State != "active" {
		t.Fatalf("member request subscription failed: %v", err)
	}

	// Admin updates/approves state
	pausedSub, err := subSvc.UpdateState(ctx, adminActor, sub.ID, "paused")
	if err != nil || pausedSub.State != "paused" {
		t.Fatalf("admin state update failed: %v", err)
	}
}
