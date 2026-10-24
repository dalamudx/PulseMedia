package app

import (
	"context"
	"encoding/json"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/notify"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This is one production Add, import, event fanout and retaining-file removal.
// CookieCloud refresh and Bangumi identity review retain their independent
// authorization boundaries and are verified by their dedicated App suites.
func TestM10EcosystemIngestionRefreshNotifySeedingPG(t *testing.T) {
	runM10SeedingNativeScenario(t, m10SeedingNativeScenario{kind: "qbittorrent", ecosystem: true})
}
func m10JointImportedConsumers(t *testing.T, f *organizationFixture) func() {
	t.Helper()
	var mu sync.Mutex
	var refreshPaths []string
	var eventIDs []string
	var protocolErrors []string
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/emby/System/Info":
			if r.Header.Get("X-Emby-Token") != "owned-joint-emby" {
				protocolErrors = append(protocolErrors, "Emby authentication")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(w, `{"ServerName":"owned-joint","Version":"4.8.0"}`)
		case "/emby/Library/Media/Updated":
			var body struct {
				Updates []struct{ Path, UpdateType string }
			}
			if r.Method != http.MethodPost || r.Header.Get("X-Emby-Token") != "owned-joint-emby" || json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Updates) != 1 || body.Updates[0].UpdateType != "Created" {
				protocolErrors = append(protocolErrors, "exact native refresh")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			refreshPaths = append(refreshPaths, body.Updates[0].Path)
			w.WriteHeader(http.StatusNoContent)
		case "/notify":
			var body map[string]any
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || r.Header.Get("X-PulseMedia-Event-ID") == "" {
				protocolErrors = append(protocolErrors, "event-bound native notification")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			eventIDs = append(eventIDs, r.Header.Get("X-PulseMedia-Event-ID"))
			w.WriteHeader(http.StatusNoContent)
		default:
			protocolErrors = append(protocolErrors, "broad or unexpected native endpoint")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(native.Close)
	cipher, err := crypto.Encrypt(outboxTestKey, []byte("owned-joint-emby"))
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := json.Marshal(map[string]string{f.target: "/joint-library"})
	if err != nil {
		t.Fatal(err)
	}
	server := uuid.New()
	if _, err = f.q.CreateMediaServer(t.Context(), db.CreateMediaServerParams{ID: server, Name: "joint-emby", ServerType: "emby", Url: native.URL + "/emby", ApiKeyEncrypted: cipher, PathMapping: mapping}); err != nil {
		t.Fatal(err)
	}
	_, _, dispatcher := testOutboxServices(f)
	channel, err := dispatcher.CreateChannel(t.Context(), notificationFixtureAdmin(t, f), "joint-notification", "webhook", notify.ChannelConfig{URL: native.URL + "/notify", Secret: "owned-joint-notify"}, "m10-joint-create-channel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		var evidence string
		if err := f.pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('event',e.event_type,'consumer',d.consumer,'state',d.state,'attempts',d.attempts)),'[]'::jsonb)::text FROM outbox_event e LEFT JOIN outbox_delivery d ON d.event_id=e.id WHERE e.event_type='media.imported'`).Scan(&evidence); err == nil {
			t.Log("joint delivery states", evidence)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Log("joint native calls", len(refreshPaths), len(eventIDs), protocolErrors)
	})
	return func() {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		var imported, notification uuid.UUID
		waitOutboxCondition(t, ctx, func() bool {
			return f.pool.QueryRow(ctx, `SELECT e.id,b.event_id FROM outbox_event e JOIN notification_batch_member m ON m.source_event_id=e.id JOIN notification_batch b ON b.id=m.batch_id AND b.channel_id=m.channel_id WHERE e.event_type='media.imported' AND m.channel_id=$3 AND b.state='sealed' AND b.member_count=1 AND EXISTS(SELECT 1 FROM outbox_delivery d WHERE d.event_id=e.id AND d.consumer=$1 AND d.state='delivered') AND EXISTS(SELECT 1 FROM outbox_delivery d WHERE d.event_id=b.event_id AND d.consumer=$2 AND d.state='delivered')`, "mediaserver.refresh:"+server.String(), "notify:"+channel.ID.String(), channel.ID).Scan(&imported, &notification) == nil
		})
		var rel string
		if err = f.pool.QueryRow(ctx, `SELECT rel_path FROM library_file WHERE root_id=$1 AND retired_at IS NULL`, f.root).Scan(&rel); err != nil {
			t.Fatal(err)
		}
		expected := "/joint-library/" + filepath.ToSlash(rel)
		mu.Lock()
		defer mu.Unlock()
		deliveredNotifications := 0
		for _, id := range eventIDs {
			if id == notification.String() {
				deliveredNotifications++
			}
		}
		if len(protocolErrors) != 0 || len(refreshPaths) != 1 || refreshPaths[0] != expected || deliveredNotifications != 1 || imported == notification {
			t.Fatal("joint event fanout mismatch", protocolErrors, len(refreshPaths), len(eventIDs))
		}
		var unrelated int
		if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM unit_fact WHERE stage='confirmed'`).Scan(&unrelated); err != nil {
			t.Fatal(err)
		}
		if unrelated != 0 || !strings.HasPrefix(refreshPaths[0], "/joint-library/") {
			t.Fatal("refresh invented confirmation or changed mapped root")
		}
		t.Log("M10_ECOSYSTEM_JOINT_APP_PASS Add=ONCE import=REAL_HARDLINK event=ORIGINAL_IMPORT_BATCH_MEMBERSHIP Emby=EXACT_NATIVE_PATH notification=NATIVE_WEBHOOK seeding=ONE_RETAINING_FILES_REMOVE demand_and_files=UNCHANGED confirmation=NONE")
	}
}
