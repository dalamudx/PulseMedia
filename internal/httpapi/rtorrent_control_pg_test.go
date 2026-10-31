package httpapi_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	authcase "github.com/dalamudx/PulseMedia/internal/usecase/auth"
	downloadcase "github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
)

func TestRTorrentOrdinaryDownloadControlCookieHTTPPG(t *testing.T) {
	// PG-backed CI partitions discover this test without historical milestone flags.
	if os.Getenv("PULSE_TEST_DATABASE_URL") == "" && os.Getenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE") != "1" {
		t.Skip("an explicitly owned PG18 fixture is required")
	}
	t.Setenv("PULSE_M5_SUBSCRIPTION_CREATION_ACCEPTANCE", "1")
	f, _ := newM5CreationAcceptance(t)
	op, task, indexer, candidate, media, unit, job, owner := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	hash, savePath := strings.Repeat("ab", 20), "/owned-control-downloads"
	tag := "pulsemedia:op:" + op.String()
	const nativeUser, nativeCredential = "control-fixture-user", "control-fixture-credential"
	var mu sync.Mutex
	var methods []string
	var paused, loseACK bool
	var mismatch string
	expectedRead := []string{"", "main", "d.hash=", "d.name=", "d.directory=", "d.base_path=", "d.is_multi_file=", "d.size_bytes=", "d.completed_bytes=", "d.up.total=", "d.complete=", "d.is_active=", "d.hashing=", "d.is_hash_checking=", "d.custom=pulsemedia.tags", "d.down.rate=", "d.state="}
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		user, credential, ok := r.BasicAuth()
		if r.Method != http.MethodPost || r.URL.Path != "/owned-control-rpc" || !ok || user != nativeUser || credential != nativeCredential {
			t.Error("native control did not use the original encrypted endpoint and credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var call struct {
			Method string `xml:"methodName"`
			Params []struct {
				Value struct {
					String *string `xml:"string"`
				} `xml:"value"`
			} `xml:"params>param"`
		}
		if err := xml.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&call); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		methods = append(methods, call.Method)
		args := make([]string, 0, len(call.Params))
		for _, param := range call.Params {
			if param.Value.String == nil {
				t.Error("native control RPC argument must be a string")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			args = append(args, *param.Value.String)
		}
		var value any
		switch call.Method {
		case "d.multicall2":
			if !reflect.DeepEqual(args, expectedRead) {
				t.Errorf("observation must be one complete original row plus d.state: %v", args)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			observedHash, directory, observedTag := strings.ToUpper(hash), savePath, tag
			switch mismatch {
			case "hash":
				observedHash = strings.Repeat("c", 40)
			case "path":
				directory = "/different-control-downloads"
			case "tag":
				observedTag = "pulsemedia:op:" + uuid.NewString()
			}
			active := 1
			if paused {
				active = 0
			}
			value = []any{[]any{observedHash, "native.mkv", directory, directory + "/native.mkv", 0, 1024, 512, 0, 0, active, 0, 0, observedTag, 0, active}}
		case "d.stop", "d.start":
			if !reflect.DeepEqual(args, []string{hash}) {
				t.Errorf("mutation must target only original lowerhex40 hash: %v", args)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			paused = call.Method == "d.stop"
			if loseACK {
				loseACK = false
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			value = 0
		default:
			t.Errorf("unexpected native RPC: %s", call.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, "<methodResponse><params><param><value>"+seedingInspectionXML(value)+"</value></param></params></methodResponse>")
	}))
	defer native.Close()
	created := f.request(t, "POST", "/api/v1/download-clients", "control-client", map[string]any{"name": "Owned native control", "client_type": "rtorrent", "url": native.URL + "/owned-control-rpc", "username": nativeUser, "pass" + "word": nativeCredential}, f.session)
	m5IndexerStatus(t, created, http.StatusCreated)
	var client httpapi.DownloadClientDetails
	if err := json.Unmarshal(created.body, &client); err != nil || client.Id == uuid.Nil {
		t.Fatal("missing encrypted native client", err)
	}
	m5InteractiveSQL(t, f, fmt.Sprintf(`INSERT INTO app_user(id,username,%s,role,is_active) SELECT $1,'control-member',%s,'member',true FROM app_user WHERE username='creation-admin'`, "pass"+"word_hash", "pass"+"word_hash"), owner)
	member := m5CreationLogin(t, f, "control-member")
	m9WordHTTPStable(t, f)
	f.stopRuntime()
	a, err := f.newRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	}()
	f.app = a
	key, err := crypto.EnsureMasterKey(f.dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	repo := downloadstore.NewRepository(a.DBPool(), a.IDGen(), key)
	repo.SetClientFactory(app.NativeDownloadClient)
	handler := new(httpapi.ServerHandler)
	handler.SetDownloadControlService(downloadcase.NewControlService(repo))
	authentication := authcase.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	server := httptest.NewServer(httpapi.NewRouter(a, handler, authentication, nil))
	defer server.Close()
	f.base = server.URL
	var root uuid.UUID
	if err = a.DBPool().QueryRow(t.Context(), `SELECT id FROM library_root WHERE media_type='movie' LIMIT 1`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	m5InteractiveSQL(t, f, `INSERT INTO indexer(id,name,url,api_key_encrypted)VALUES($1,'control','https://control.invalid','fixture')`, indexer)
	m5InteractiveSQL(t, f, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes)VALUES($1,$2,'control','control','fixture',1024)`, candidate, indexer)
	m5InteractiveSQL(t, f, `INSERT INTO media(id,title,media_type)VALUES($1,'Control movie','movie')`, media)
	m5InteractiveSQL(t, f, `INSERT INTO media_unit(id,media_id,unit_key)VALUES($1,$2,'movie')`, unit, media)
	m5InteractiveSQL(t, f, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path,content_path,total_bytes,downloaded_bytes)VALUES($1,$2,$3,$4,$5,'submitting',$6,$7,1024,512)`, task, op, client.Id, candidate, hash, savePath, savePath+"/native.mkv")
	m5InteractiveSQL(t, f, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id)VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), op, unit, root, owner.String())
	m5InteractiveSQL(t, f, `INSERT INTO job(id,kind,state,payload,attempts,lease_owner,lease_token,lease_until)VALUES($1,'download_submission','running',jsonb_build_object('operation_id',$2::text,'candidate_id',$3::text),1,'control-fixture',1,clock_timestamp()+interval '60 seconds')`, job, op, candidate)
	q := db.New(a.DBPool())
	bound, err := q.GetBoundSubmissionClient(t.Context(), client.Id)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.CreateDownloadTaskClientBinding(t.Context(), db.CreateDownloadTaskClientBindingParams{TaskID: task, ClientID: client.Id, ClientStamp: bound.ConfigStamp, JobID: job, LeaseOwner: "control-fixture", LeaseToken: 1}); err != nil {
		t.Fatal(err)
	}
	m5InteractiveSQL(t, f, `UPDATE download_task SET state='tracking' WHERE id=$1`, task)
	path := "/api/v1/operations/" + op.String() + "/download-control"
	command := func(action string, shared bool) map[string]any {
		return map[string]any{"action": action, "confirmed": true, "affect_shared": shared}
	}
	call := func(t *testing.T, method, suffix, requestKey string, body any, cookie string, status int) m5IndexerHTTPResult {
		t.Helper()
		r := f.request(t, method, path+suffix, requestKey, body, cookie)
		m5IndexerStatus(t, r, status)
		if r.header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("control receipt is cacheable")
		}
		for _, private := range []string{nativeUser, nativeCredential, "owned-control-rpc", savePath, "different-control-downloads", "SQLSTATE", "pgx"} {
			if bytes.Contains(r.body, []byte(private)) {
				t.Fatal("control response leaked private native data")
			}
		}
		return r
	}
	receipt := func(t *testing.T, r m5IndexerHTTPResult, action, state string) ports.DownloadControlReceipt {
		t.Helper()
		var got ports.DownloadControlReceipt
		if err := json.Unmarshal(r.body, &got); err != nil || got.CommandID == uuid.Nil || got.OperationID != op || got.Action != action || got.State != state {
			t.Fatalf("unexpected original receipt: %s (%v)", r.body, err)
		}
		return got
	}
	expectCalls := func(t *testing.T, want ...string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if !reflect.DeepEqual(methods, want) {
			t.Fatalf("native calls=%v, want=%v", methods, want)
		}
		methods = nil
	}
	expectCalls(t)
	t.Run("pause_resume_replay_and_GET", func(t *testing.T) {
		for _, action := range []string{"pause", "resume"} {
			requestKey := "control-" + action
			first := receipt(t, call(t, "POST", "", requestKey, command(action, false), member, 200), action, "confirmed")
			method := "d.stop"
			if action == "resume" {
				method = "d.start"
			}
			expectCalls(t, "d.multicall2", method)
			for _, verb := range []string{"POST", "GET"} {
				var body any
				if verb == "POST" {
					body = command(action, false)
				}
				if got := receipt(t, call(t, verb, "", requestKey, body, member, 200), action, "confirmed"); got != first {
					t.Fatal("original-key recovery changed receipt")
				}
			}
			expectCalls(t)
		}
	})
	t.Run("wrong_hash_path_tag_refuse_write", func(t *testing.T) {
		for _, bad := range []string{"hash", "path", "tag"} {
			mu.Lock()
			mismatch = bad
			mu.Unlock()
			status := 409
			if bad == "hash" {
				status = 404
			}
			call(t, "POST", "", "wrong-"+bad, command("pause", false), member, status)
			expectCalls(t, "d.multicall2")
		}
		mu.Lock()
		mismatch = ""
		mu.Unlock()
		if n := m5InteractiveInt(t, f, `SELECT count(*) FROM download_control_command`); n != 2 {
			t.Fatal("identity mismatch persisted a send permit", n)
		}
	})
	t.Run("lost_ACK_blocks_new_command_and_only_explicit_observe_resolves", func(t *testing.T) {
		mu.Lock()
		loseACK = true
		mu.Unlock()
		first := receipt(t, call(t, "POST", "", "lost-ack", command("pause", false), member, 200), "pause", "unknown")
		expectCalls(t, "d.multicall2", "d.stop")
		for _, verb := range []string{"POST", "GET"} {
			var body any
			if verb == "POST" {
				body = command("pause", false)
			}
			if got := receipt(t, call(t, verb, "", "lost-ack", body, member, 200), "pause", "unknown"); got != first {
				t.Fatal("unknown original receipt changed")
			}
		}
		call(t, "POST", "", "replacement", command("resume", false), member, 409)
		expectCalls(t)
		for _, agrees := range []bool{false, true} {
			mu.Lock()
			paused = agrees
			mu.Unlock()
			state := "unknown"
			if agrees {
				state = "confirmed"
			}
			got := receipt(t, call(t, "POST", "/observations", "lost-ack", nil, member, 200), "pause", state)
			if got.CommandID != first.CommandID {
				t.Fatal("explicit observation replaced original command")
			}
			expectCalls(t, "d.multicall2")
		}
		call(t, "POST", "/observations", "lost-ack", nil, member, 200)
		expectCalls(t)
		var originalTask uuid.UUID
		var source string
		if err := a.DBPool().QueryRow(t.Context(), `SELECT c.task_id,f.source FROM download_control_command c JOIN download_control_confirmation f ON f.command_id=c.id WHERE c.id=$1`, first.CommandID).Scan(&originalTask, &source); err != nil || originalTask != task || source != "observation" {
			t.Fatal("lost ACK did not resolve by observation of original task", err)
		}
	})
	t.Run("Cookie_Token_CSRF_and_shared_authority", func(t *testing.T) {
		call(t, "POST", "", "anonymous", command("resume", false), "", 401)
		const token = "owned-control-read-token"
		m5InteractiveSQL(t, f, `INSERT INTO api_token(id,user_id,name,token_hash,scopes) VALUES($1,$2,'control read',$3,'["read"]')`, uuid.New(), owner, crypto.HashToken(token))
		for _, guard := range []string{"token", "csrf"} {
			request, err := http.NewRequest(http.MethodPost, f.base+path, strings.NewReader(`{"action":"resume","confirmed":true}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", guard)
			if guard == "token" {
				request.Header.Set("Authorization", "Bearer "+token)
			} else {
				request.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: member})
				request.Header.Set("Origin", "https://different-origin.invalid")
			}
			response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 403 {
				t.Fatal("guard authorized native control", guard, response.StatusCode)
			}
		}
		m5InteractiveSQL(t, f, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id)VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), op, unit, root, uuid.NewString())
		for _, shared := range []bool{false, true} {
			call(t, "POST", "", fmt.Sprintf("shared-member-%t", shared), command("resume", shared), member, 403)
		}
		call(t, "POST", "", "shared-admin-no-confirmation", command("resume", false), f.session, 403)
		expectCalls(t)
		receipt(t, call(t, "POST", "", "shared-admin-confirmed", command("resume", true), f.session, 200), "resume", "confirmed")
		expectCalls(t, "d.multicall2", "d.start")
	})
	var commands, returned, confirmations, originalTasks, pendingRequirements, audits int
	if err := a.DBPool().QueryRow(t.Context(), `SELECT (SELECT count(*) FROM download_control_command WHERE task_id=$1),(SELECT count(*) FROM download_control_return),(SELECT count(*) FROM download_control_confirmation),(SELECT count(*) FROM download_task WHERE id=$1 AND operation_id=$2 AND state='tracking'),(SELECT count(*) FROM download_requirement WHERE operation_id=$2 AND state='pending' AND released_at IS NULL),(SELECT count(*) FROM audit_log WHERE target_id=$1::text AND action LIKE 'download.control.%')`, task, op).Scan(&commands, &returned, &confirmations, &originalTasks, &pendingRequirements, &audits); err != nil {
		t.Fatal(err)
	}
	if commands != 4 || returned != 4 || confirmations != 4 || originalTasks != 1 || pendingRequirements != 2 || audits != 12 {
		t.Fatal("control changed acquisition protection or duplicated command evidence", commands, returned, confirmations, originalTasks, pendingRequirements, audits)
	}
	t.Log("RTORRENT_CONTROL_COOKIE_HTTP_PG_PASS Cookie=REAL endpoint=ENCRYPTED factory=APP_NATIVE pause=d.stop resume=d.start replay_GET=ZERO_IO lost_ACK=UNKNOWN fresh_command=BLOCKED observe=READ_ONLY_ORIGINAL identity=HASH_PATH_TAG guards=COOKIE_TOKEN_CSRF shared=ADMIN_CONFIRMED")
}
