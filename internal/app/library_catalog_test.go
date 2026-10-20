package app

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
	"github.com/dalamudx/PulseMedia/internal/usecase/media_library"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestLibraryCatalogNativeAndCookie(t *testing.T) {
	runFullMoviePipelineSource(t, true, "", "", func(f *organizationFixture, actor *ports.Actor, _ uuid.UUID, a *App) func() {
		return func() {
			ctx := context.Background()
			svc := media_library.NewService(a.LibraryCatalogReader())
			page, e := svc.List(ctx, actor, ports.LibraryCatalogRequest{Limit: 1})
			if e != nil || len(page.Items) != 1 || !page.Items[0].Imported {
				t.Fatal("native import catalog", page, e)
			}
			var root uuid.UUID
			var path string
			if e = f.pool.QueryRow(ctx, "SELECT id,path FROM library_root WHERE media_type='tv' LIMIT 1").Scan(&root, &path); e != nil {
				t.Fatal(e)
			}
			if filepath.Clean(path) != filepath.Join(a.Config().DataDir, "media", "tv") {
				t.Fatal("TV fixture root outside App data directory", path)
			}
			media := uuid.New()
			if _, e = f.pool.Exec(ctx, "INSERT INTO media(id,title,media_type) VALUES($1,'Native Series','tv')", media); e != nil {
				t.Fatal(e)
			}
			var retired uuid.UUID
			for _, unitKey := range []string{"S01E02", "S01E10", "S02E01", "S03E07"} {
				unit, file := uuid.New(), uuid.New()
				relative := filepath.Join("Catalog Series", unitKey+".mkv")
				absolute := filepath.Join(path, relative)
				if e = os.MkdirAll(filepath.Dir(absolute), 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(absolute, []byte("catalog-video"), 0600); e != nil {
					t.Fatal(e)
				}
				stat, err := os.Stat(absolute)
				if err != nil {
					t.Fatal(err)
				}
				commands := []struct {
					sql  string
					args []any
				}{{"INSERT INTO media_unit(id,media_id,unit_key) VALUES($1,$2,$3)", []any{unit, media, unitKey}}, {"INSERT INTO library_file(id,root_id,rel_path,size_bytes,mtime_ns) VALUES($1,$2,$3,$4,$5)", []any{file, root, relative, stat.Size(), stat.ModTime().UnixNano()}}, {"INSERT INTO library_file_unit(file_id,unit_id) VALUES($1,$2)", []any{file, unit}}}
				for _, c := range commands {
					if _, err = f.pool.Exec(ctx, c.sql, c.args...); err != nil {
						t.Fatal(err)
					}
				}
				if unitKey == "S03E07" {
					retired = file
				}
			}
			if _, e = f.pool.Exec(ctx, "UPDATE library_file SET retired_at=clock_timestamp() WHERE id=$1", retired); e != nil {
				t.Fatal(e)
			}
			seen := map[string]bool{}
			cursor := ""
			for {
				p, err := svc.List(ctx, actor, ports.LibraryCatalogRequest{Limit: 1, Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				for _, v := range p.Items {
					key := v.FileID.String() + v.UnitID.String()
					if seen[key] || v.FileID == retired {
						t.Fatal("duplicate/retired file", v)
					}
					seen[key] = true
				}
				if p.NextCursor == "" {
					break
				}
				cursor = p.NextCursor
			}
			if len(seen) != 4 {
				t.Fatal("actual mapped count", len(seen))
			}
			other, e := f.q.CreateUser(ctx, db.CreateUserParams{ID: uuid.New(), Username: "catalog-other", PasswordHash: "fixture", Role: "member", IsActive: true})
			if e != nil {
				t.Fatal(e)
			}
			otherActor := &ports.Actor{Type: ports.ActorTypeUser, ID: other.ID.String(), Role: ports.RoleMember}
			if _, e = svc.List(ctx, otherActor, ports.LibraryCatalogRequest{Cursor: cursor}); !errors.Is(e, ports.ErrInvalidLibraryQuery) {
				t.Fatal("borrowed cursor", e)
			}
			wrong := *actor
			wrong.Role = ports.RoleAdmin
			if _, e = svc.List(ctx, &wrong, ports.LibraryCatalogRequest{}); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("stale role", e)
			}
			tokenActor := *actor
			tokenActor.Scopes = []string{"write"}
			if _, e = svc.List(ctx, &tokenActor, ports.LibraryCatalogRequest{}); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("write scope borrowed", e)
			}
			tokenActor.Scopes = []string{"read"}
			if _, e = svc.List(ctx, &tokenActor, ports.LibraryCatalogRequest{}); e != nil {
				t.Fatal("read scope", e)
			}
			if os.Getenv("PULSE_UI_FIXTURE") == "1" {
				runLibraryCatalogCookie(t, ctx, f, a, actor)
			}
		}
	})
}
func runLibraryCatalogCookie(t *testing.T, ctx context.Context, f *organizationFixture, a *App, actor *ports.Actor) {
	t.Helper()
	token, hash, e := crypto.GenerateToken("pm_sess_")
	if e != nil {
		t.Fatal(e)
	}
	_, csrf, e := crypto.GenerateToken("pm_csrf_")
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.q.CreateSession(ctx, db.CreateSessionParams{ID: uuid.New(), UserID: uuid.MustParse(actor.ID), TokenHash: hash, CsrfTokenHash: csrf, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}})
	if e != nil {
		t.Fatal(e)
	}
	authSvc := auth.NewService(postgres.NewAuthenticationStore(f.pool), crypto.AuthenticationSecrets{}, clock.New(), f.gen)
	h := httpapi.NewServerHandler(a, authSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	hub := httpapi.NewSSEHub()
	router := httpapi.NewRouter(a, h, authSvc, hub)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/disable" {
			if _, err := f.pool.Exec(r.Context(), "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(actor.ID)); err != nil {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(204)
			return
		}
		router.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer hub.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	chrome := exec.CommandContext(browserCtx, "/opt/google/chrome/chrome", "--headless=new", "--no-sandbox", "--disable-dev-shm-usage", "--remote-debugging-port=9222", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome"))
	chrome.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	chrome.Cancel = func() error { return syscall.Kill(-chrome.Process.Pid, syscall.SIGKILL) }
	chrome.Stdout = io.Discard
	chrome.Stderr = io.Discard
	if e = chrome.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = syscall.Kill(-chrome.Process.Pid, syscall.SIGKILL); _ = chrome.Wait() }()
	cmd := exec.CommandContext(browserCtx, "node", "tests/library-catalog-browser.mjs")
	cmd.Dir = "../../web"
	cmd.Env = append(os.Environ(), "PULSE_LIBRARY_BASE="+server.URL, "PULSE_LIBRARY_SESSION="+token)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("library cookie: %v %s", e, out)
	}
	t.Log(string(out))
}
