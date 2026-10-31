package httpapi_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	downloadstore "github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	authcase "github.com/dalamudx/PulseMedia/internal/usecase/auth"
	downloadcase "github.com/dalamudx/PulseMedia/internal/usecase/download"
	"github.com/google/uuid"
)

func seedingInspectionXML(value any) string {
	switch v := value.(type) {
	case string:
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(v))
		return "<string>" + b.String() + "</string>"
	case int:
		return "<i8>" + strconv.Itoa(v) + "</i8>"
	case []any:
		var b strings.Builder
		b.WriteString("<array><data>")
		for _, item := range v {
			b.WriteString("<value>" + seedingInspectionXML(item) + "</value>")
		}
		b.WriteString("</data></array>")
		return b.String()
	default:
		panic("unsupported test XML value")
	}
}

func TestSeedingInspectionCookieRTorrentHTTPPG(t *testing.T) {
	f, _ := newM5CreationAcceptance(t)
	op, task, indexer, candidate, media, unit, job := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	hash, tag := strings.Repeat("a", 40), "pulsemedia:op:"+op.String()
	var calls atomic.Int32
	var bad atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8192))
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "inspection-native-user" || password != "inspection-native-password" || r.URL.Path != "/private-inspection-rpc" {
			t.Error("original native endpoint credentials changed")
			w.WriteHeader(401)
			return
		}
		var call struct {
			Method string `xml:"methodName"`
		}
		if xml.Unmarshal(raw, &call) != nil || call.Method != "d.multicall2" || !bytes.Contains(raw, []byte("d.ratio=")) {
			t.Error("manual inspection used a write, free-space request or wrong RPC")
			w.WriteHeader(400)
			return
		}
		path := "/private-inspection-downloads"
		if bad.Load() {
			path = "/wrong-native-identity"
		}
		row := []any{strings.ToUpper(hash), "native.mkv", path, path + "/native.mkv", 0, 1024, 1024, 2048, 1, 1, 0, 0, tag, 0, 2500}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, "<methodResponse><params><param><value>"+seedingInspectionXML([]any{row})+"</value></param></params></methodResponse>")
	}))
	defer remote.Close()
	created := f.request(t, "POST", "/api/v1/download-clients", "inspection-client", map[string]any{"name": "inspection native", "client_type": "rtorrent", "url": remote.URL + "/private-inspection-rpc", "username": "inspection-native-user", "password": "inspection-native-password"}, f.session)
	m5IndexerStatus(t, created, 201)
	var client httpapi.DownloadClientDetails
	if json.Unmarshal(created.body, &client) != nil || client.Id == uuid.Nil {
		t.Fatal("missing native client")
	}
	owner := uuid.New()
	m5InteractiveSQL(t, f, `INSERT INTO app_user(id,username,password_hash,role,is_active) SELECT $1,'inspection-member',password_hash,'member',true FROM app_user WHERE username='creation-admin'`, owner)
	session := m5CreationLogin(t, f, "inspection-member")
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
	handler.SetSeedingInspectionService(downloadcase.NewSeedingInspectionService(repo, a.Clock()))
	authentication := authcase.NewService(postgres.NewAuthenticationStore(a.DBPool()), crypto.AuthenticationSecrets{}, a.Clock(), a.IDGen())
	server := httptest.NewServer(httpapi.NewRouter(a, handler, authentication, nil))
	defer server.Close()
	f.base = server.URL
	var root uuid.UUID
	if err = a.DBPool().QueryRow(t.Context(), `SELECT id FROM library_root WHERE media_type='movie' LIMIT 1`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	m5InteractiveSQL(t, f, `INSERT INTO indexer(id,name,url,api_key_encrypted)VALUES($1,'inspection','https://inspection.invalid','fixture')`, indexer)
	m5InteractiveSQL(t, f, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes)VALUES($1,$2,'inspection','inspection','fixture',1024)`, candidate, indexer)
	m5InteractiveSQL(t, f, `INSERT INTO media(id,title,media_type)VALUES($1,'Inspection movie','movie')`, media)
	m5InteractiveSQL(t, f, `INSERT INTO media_unit(id,media_id,unit_key)VALUES($1,$2,'movie')`, unit, media)
	m5InteractiveSQL(t, f, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path,content_path,total_bytes,downloaded_bytes)VALUES($1,$2,$3,$4,$5,'submitting','/private-inspection-downloads','/private-inspection-downloads/native.mkv',1024,0)`, task, op, client.Id, candidate, hash)
	m5InteractiveSQL(t, f, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id)VALUES($1,$2,$3,$4,'user',$5)`, uuid.New(), op, unit, root, owner.String())
	m5InteractiveSQL(t, f, `INSERT INTO job(id,kind,state,payload,attempts,lease_owner,lease_token,lease_until)VALUES($1,'download_submission','running',jsonb_build_object('operation_id',$2::text,'candidate_id',$3::text),1,'inspection-fixture',1,clock_timestamp()+interval '60 seconds')`, job, op, candidate)
	q := db.New(a.DBPool())
	bound, err := q.GetBoundSubmissionClient(t.Context(), client.Id)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.CreateDownloadTaskClientBinding(t.Context(), db.CreateDownloadTaskClientBindingParams{TaskID: task, ClientID: client.Id, ClientStamp: bound.ConfigStamp, JobID: job, LeaseOwner: "inspection-fixture", LeaseToken: 1}); err != nil {
		t.Fatal(err)
	}
	m5InteractiveSQL(t, f, `UPDATE download_task SET state='tracking' WHERE id=$1`, task)
	before := m9MediaRequestState(t, f)
	path := "/api/v1/operations/" + op.String() + "/seeding-observations"
	call := func(body any, cookie string, status int) m5IndexerHTTPResult {
		t.Helper()
		r := f.request(t, "POST", path, "", body, cookie)
		m5IndexerStatus(t, r, status)
		if r.header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("manual read cacheable")
		}
		for _, secret := range []string{"private-inspection", "inspection-native-user", "inspection-native-password", "wrong-native-identity", "SQLSTATE", "pgx"} {
			if bytes.Contains(r.body, []byte(secret)) {
				t.Fatal("manual read leaked private native data")
			}
		}
		return r
	}
	result := call(map[string]any{}, session, 200)
	var facts struct {
		Complete bool     `json:"complete"`
		Ratio    *float64 `json:"share_ratio"`
		Seconds  *string  `json:"seeding_seconds"`
		Infinite bool     `json:"infinite_share_ratio"`
	}
	if json.Unmarshal(result.body, &facts) != nil || !facts.Complete || facts.Ratio == nil || *facts.Ratio != 2.5 || facts.Seconds != nil || facts.Infinite {
		t.Fatal("native facts guessed or lost", string(result.body))
	}
	call(nil, f.session, 200)
	call(`{"owner_id":"other"}`, session, 400)
	call(nil, "", 401)
	for _, suffix := range []string{"", "/"} {
		req, err := http.NewRequest(http.MethodPost, f.base+path+suffix, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: session})
		req.Header.Set("Origin", "https://different-origin.invalid")
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatal("CSRF denial request failed", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusForbidden || response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatal("early CSRF denial cacheable", response.StatusCode, suffix)
		}
	}
	bad.Store(true)
	failed := call(nil, session, 503)
	if bytes.Contains(failed.body, []byte("share_ratio")) {
		t.Fatal("native identity failure exposed previous facts")
	}
	if calls.Load() != 3 {
		t.Fatal("manual read dispatch count", calls.Load())
	}
	if !reflect.DeepEqual(before, m9MediaRequestState(t, f)) {
		t.Fatal("inspection changed persisted business, permits, telemetry or scheduling")
	}
}
