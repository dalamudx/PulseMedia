package postgres_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type trackingFixture struct {
	*nativeUpgradeFixture
	repo                           ports.MovieUpgradeTrackingRepository
	adds, infos, files, exports    atomic.Int32
	complete, wrongHash, wrongFile atomic.Bool
	issuer                         *postgres.MovieUpgradeTrackingQueryVerifier
	driver                         ports.MovieUpgradeNativeTrackingDriver
	onInfo                         atomic.Pointer[func()]
}

func nativeTrackingFixture(t *testing.T, ack string, beforeFreeze ...func(*frozenUpgradeFixture)) *trackingFixture {
	t.Helper()
	f := &trackingFixture{}
	f.complete.Store(true)
	var save string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "tracking-test", Path: "/"})
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/add":
			f.adds.Add(1)
			reader, e := r.MultipartReader()
			if e != nil {
				t.Error(e)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var raw []byte
			for {
				p, e := reader.NextPart()
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Error(e)
					break
				}
				if p.FormName() == "torrents" {
					raw, _ = io.ReadAll(io.LimitReader(p, 10<<20))
				}
				_ = p.Close()
			}
			if string(raw) != string(f.proof.Bytes()) {
				t.Error("changed original Add bytes")
			}
			_, _ = io.WriteString(w, ack)
		case "/api/v2/torrents/info":
			f.infos.Add(1)
			if hook := f.onInfo.Load(); hook != nil {
				(*hook)()
			}
			hash := f.proof.InfoHash()
			if f.wrongHash.Load() {
				hash = "ffffffffffffffffffffffffffffffffffffffff"
			}
			progress := 0.5
			state := "downloading"
			size := f.proof.TotalBytes()
			done := size / 2
			if f.complete.Load() {
				progress = 1
				state = "uploading"
				done = size
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"hash": hash, "tags": "pulsemedia:op:" + f.operation.String(), "save_path": save, "size": size, "downloaded": done, "progress": progress, "state": state}})
		case "/api/v2/torrents/export":
			f.exports.Add(1)
			_, _ = w.Write(f.proof.Bytes())
		case "/api/v2/torrents/files":
			f.files.Add(1)
			listing := []map[string]any{}
			for i, file := range f.proof.Files() {
				name := file.Path
				if f.wrongFile.Load() {
					name = "Other.Movie.2024.mkv"
				}
				listing = append(listing, map[string]any{"index": i, "name": name, "size": file.Size, "progress": 1})
			}
			_ = json.NewEncoder(w).Encode(listing)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	f.nativeUpgradeFixture = nativeUpgrade(t, "Ok.", false, func(b *frozenUpgradeFixture) {
		if _, e := b.p.Exec(context.Background(), `UPDATE download_client SET url=$2 WHERE id=$1`, b.client, server.URL); e != nil {
			t.Fatal(e)
		}
		for _, hook := range beforeFreeze {
			hook(b)
		}
	})
	if e := f.p.QueryRow(context.Background(), `SELECT evidence->>'save_path' FROM movie_upgrade_preparation WHERE operation_id=$1`, f.operation).Scan(&save); e != nil {
		t.Fatal(e)
	}
	_, command, sender, request := sendJobFixtureFromNative(t, f.nativeUpgradeFixture)
	queued, e := command.QueueMovieUpgradeSend(context.Background(), f.actor, request, "tracking-explicit-send")
	if e != nil {
		t.Fatalf("queue native tracking fixture: %v", e)
	}
	result, e := sender.SendMovieUpgradeJob(context.Background(), f.actor, claimSendJob(t, f.nativeUpgradeFixture, queued), f.proof)
	wantStatus := "confirmed"
	if ack != "Ok." {
		wantStatus = "unknown"
	}
	if e != nil || result.Status != wantStatus {
		t.Fatal(result, e)
	}
	if version, e := postgres.Migrate(context.Background(), f.dsn, tracking72MigrationDir(t)); e != nil || version != 72 {
		t.Fatal(version, e)
	}
	issuer := postgres.NewMovieUpgradeTrackingQueryVerifier()
	driver, e := download.NewMovieUpgradeNativeTrackingDriver(f.manager, issuer, torrent.Inspector{}, nativeUpgradeClock{})
	if e != nil {
		t.Fatal(e)
	}
	f.issuer, f.driver = issuer, driver
	f.repo, e = postgres.NewMovieUpgradeNativeTrackingRepository(f.preparation, driver, issuer)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func TestMovieUpgradeTrackingNativePG(t *testing.T) {
	for _, mode := range []string{"completed", "tracking", "wrong_hash", "wrong_file"} {
		t.Run(mode, func(t *testing.T) {
			f := nativeTrackingFixture(t, "Ok.")
			if mode == "tracking" {
				f.complete.Store(false)
			}
			f.wrongHash.Store(mode == "wrong_hash")
			f.wrongFile.Store(mode == "wrong_file")
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			result, e := f.repo.TrackMovieUpgrade(context.Background(), f.actor, f.operation, uuid.New(), "tracking-admit", "tracking-return")
			want := mode
			if mode == "wrong_hash" || mode == "wrong_file" {
				want = "manual_review"
			}
			if e != nil || result == nil || result.Outcome != want || result.ExecutionAllowed {
				t.Fatal(result, e)
			}
			if mode == "completed" && result.Disposition != "organization_review_required" {
				t.Fatal(result)
			}
			after := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			if string(before) != string(after) {
				t.Fatal("tracking changed original task/files/facts/protection")
			}
			if f.adds.Load() != 1 {
				t.Fatal("unexpected repeat Add", f.adds.Load())
			}
			queries := f.infos.Load()
			if queries == 0 {
				t.Fatal("no native query")
			}
			if mode == "completed" && (queries != 2 || f.files.Load() != 1 || f.exports.Load() != 1) {
				t.Fatal(queries, f.files.Load(), f.exports.Load())
			}
			recovered, e := f.repo.TrackMovieUpgrade(context.Background(), f.actor, f.operation, uuid.New(), "tracking-admit", "tracking-return")
			a, _ := json.Marshal(result)
			b, _ := json.Marshal(recovered)
			if e != nil || string(a) != string(b) || f.infos.Load() != queries {
				t.Fatal("historical recovery", recovered, e, f.infos.Load())
			}
		})
	}
}
