package plex_test

import (
	"errors"
	"github.com/dalamudx/PulseMedia/internal/adapter/mediaserver/plex"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlexPreciseRefreshRejectsUnmappedAndAmbiguous(t *testing.T) {
	sections := `{"MediaContainer":{"Directory":[{"key":"1","Location":[{"path":"/movies"}]},{"key":"2","Location":[{"path":"/movies/owned"}]}]}}`
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/library/sections" {
			_, _ = io.WriteString(w, sections)
			return
		}
		calls++
		if r.URL.Path != "/library/sections/2/refresh" || r.URL.Query().Get("path") != "/movies/owned" {
			t.Error("wrong library scope")
		}
		w.WriteHeader(204)
	}))
	defer s.Close()
	c := plex.NewClient(s.URL, "owned", s.Client())
	for _, paths := range [][]string{{"/movies2/file.mkv"}, {"/movies/owned/file.mkv", "/unmapped/file.mkv"}, {"/movies/../else/file.mkv"}, {"relative.mkv"}} {
		if e := c.RefreshPaths(t.Context(), paths); !errors.Is(e, ports.ErrBadResponse) || calls != 0 {
			t.Fatal("unmapped path caused broad or partial refresh", e, calls)
		}
	}
	if e := c.RefreshPaths(t.Context(), []string{"/movies/owned/a.mkv", "/movies/owned/b.mkv"}); e != nil || calls != 1 {
		t.Fatal("wrong precise refresh", e, calls)
	}
	sections = `{"MediaContainer":{"Directory":[{"key":"1","Location":[{"path":"/movies"}]},{"key":"2","Location":[{"path":"/movies"}]}]}}`
	if e := c.RefreshPaths(t.Context(), []string{"/movies/a.mkv"}); !errors.Is(e, ports.ErrBadResponse) || calls != 1 {
		t.Fatal("ambiguous library refreshed", e)
	}
}
