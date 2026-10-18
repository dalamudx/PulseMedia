package acquisition_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
	"testing"
)

func mutateCommandUser(t *testing.T, f commandFixture, mode string) {
	t.Helper()
	var e error
	switch mode {
	case "demoted":
		_, e = f.pool.Exec(context.Background(), "UPDATE app_user SET role='member' WHERE id=$1", f.actor.ID)
	case "disabled":
		_, e = f.pool.Exec(context.Background(), "UPDATE app_user SET is_active=false WHERE id=$1", f.actor.ID)
	case "deleted":
		_, e = f.pool.Exec(context.Background(), "DELETE FROM app_user WHERE id=$1", f.actor.ID)
	case "token":
		f.actor.Scopes = []string{"read"}
	case "system":
		f.actor.Type = ports.ActorTypeSystem
	case "missing":
		f.actor.ID = uuid.NewString()
	case "invalid":
		f.actor.ID = "invalid-actor"
	}
	if e != nil {
		t.Fatal(e)
	}
}
func TestAcquireUnkeyedRejectsStaleOrNonSessionActor(t *testing.T) {
	for _, mode := range []string{"demoted", "disabled", "deleted", "token", "system", "missing", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommandFixture(t)
			f.cmd.IdempotencyKey = ""
			before := commandCounts(t, f)
			mutateCommandUser(t, f, mode)
			if _, e := f.svc.AcquireMovie(context.Background(), f.actor, f.cmd); !errors.Is(e, ports.ErrForbidden) {
				t.Fatalf("unkeyed authorization: %v", e)
			}
			if before != commandCounts(t, f) {
				t.Fatal("denial left business state")
			}
		})
	}
}
func authorizationSelectionFixture(t *testing.T) (commandFixture, *acquisition.MovieSelectionService, *selectedMetadataFixture, ports.MovieSelection) {
	t.Helper()
	f := newCommandFixture(t)
	ctx := context.Background()
	q := db.New(f.pool)
	client := uuid.New()
	if _, e := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "selection-client", ClientType: "qbittorrent", Url: "http://127.0.0.1:8080", PasswordEncrypted: []byte("cipher"), RemotePathMap: []byte("{}")}); e != nil {
		t.Fatal(e)
	}
	profile := []byte(`{"resolution":"1080p","resolution_parser_version":"resolution.tokens.v1","source":"Bluray","source_parser_version":"source.tokens.v2","codec":"H264","codec_parser_version":"codec.tokens.v2"}`)
	var idx uuid.UUID
	if e := f.pool.QueryRow(ctx, "UPDATE release_candidate SET title='Fight.Club.1999.1080p.Bluray.x264',size_bytes=1024,quality_profile=$2 WHERE id=$1 RETURNING indexer_id", f.cmd.CandidateID, profile).Scan(&idx); e != nil {
		t.Fatal(e)
	}
	query := uuid.New()
	if _, e := f.pool.Exec(ctx, "INSERT INTO indexer_query(id,indexer_id,query_type,keyword,config_version,state,finished_at) VALUES($1,$2,'search','Fight Club',1,'succeeded',clock_timestamp())", query, idx); e != nil {
		t.Fatal(e)
	}
	if e := q.LinkQueryCandidate(ctx, db.LinkQueryCandidateParams{QueryID: query, CandidateID: f.cmd.CandidateID}); e != nil {
		t.Fatal(e)
	}
	provider := &selectedMetadataFixture{}
	svc := acquisition.NewMovieSelectionService(postgres.NewAcquisitionStore(f.pool, idgen.New(), clock.New(), acquisition.SelectMovieRound), provider, clock.New())
	return f, svc, provider, ports.MovieSelection{TMDBID: 550, RootID: f.cmd.RootID, CandidateID: f.cmd.CandidateID, DownloaderID: client, PolicyID: "default"}
}
func TestSelectedMovieCurrentUserBeforeNetworkAndReceipt(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, mode := range []string{"demoted", "disabled", "deleted", "token", "system", "missing", "invalid"} {
			t.Run(mode+map[bool]string{false: "-preview", true: "-replay"}[replay], func(t *testing.T) {
				f, svc, provider, c := authorizationSelectionFixture(t)
				ctx := context.Background()
				cmd := ports.MovieSelectionCommand{Selection: c, Confirmed: true}
				if replay {
					p, e := svc.Preview(ctx, f.actor, c)
					if e != nil || p.Decision != "accept" {
						t.Fatal(p, e)
					}
					cmd.ExpectedInputStamp = p.InputStamp
					if _, e = svc.Commit(ctx, f.actor, cmd, "auth-once"); e != nil {
						t.Fatal(e)
					}
				}
				before := commandCounts(t, f)
				calls := provider.calls.Load()
				mutateCommandUser(t, f, mode)
				var e error
				if replay {
					_, e = svc.Commit(ctx, f.actor, cmd, "auth-once")
				} else {
					_, e = svc.Preview(ctx, f.actor, c)
				}
				if !errors.Is(e, ports.ErrForbidden) || provider.calls.Load() != calls || before != commandCounts(t, f) {
					t.Fatalf("authorization/network/state: %v calls %d -> %d", e, calls, provider.calls.Load())
				}
			})
		}
	}
}
