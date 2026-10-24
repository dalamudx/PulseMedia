package httpapi_test

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
)

func TestM11RTorrentConfigurationCookiePG(t *testing.T) {
	f := newM5EndpointAcceptance(t)
	var calls atomic.Int32
	var fail atomic.Bool
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, password, ok := r.BasicAuth()
		var request struct {
			Method string `xml:"methodName"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/RPC2" || !ok || user != "native-user" || password != "endpoint-secret-one" || xml.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&request) != nil || request.Method != "system.client_version" {
			t.Error("native probe was not authenticated read-only XML-RPC")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		if fail.Load() {
			_, _ = io.WriteString(w, `<methodResponse><fault><value><struct><member><name>faultCode</name><value><int>1</int></value></member><member><name>faultString</name><value><string>owned-native-error-secret</string></value></member></struct></value></fault></methodResponse>`)
			return
		}
		_, _ = io.WriteString(w, `<methodResponse><params><param><value><string>0.9.8</string></value></param></params></methodResponse>`)
	}))
	defer native.Close()
	input := map[string]any{"name": "Owned rTorrent", "client_type": "rtorrent", "url": native.URL + "/RPC2", "username": "native-user", "password": "endpoint-secret-one"}
	created := f.request(t, "POST", "/api/v1/download-clients", input)
	m5EndpointStatus(t, created, http.StatusCreated)
	row := m5EndpointObject(t, created)
	id, ok := row["id"].(string)
	if !ok || row["client_type"] != "rtorrent" {
		t.Fatal("public native protocol lost")
	}
	path := "/api/v1/download-clients/" + id
	ciphertext := f.cipher(t, "qbittorrent", path)
	plaintext, err := crypto.Decrypt(f.key, ciphertext)
	if err != nil || string(plaintext) != "endpoint-secret-one" || bytes.Contains(ciphertext, plaintext) {
		t.Fatal("credential encryption not preserved")
	}
	m5EndpointStatus(t, f.request(t, "GET", "/api/v1/download-clients", nil), http.StatusOK)
	if calls.Load() != 0 {
		t.Fatal("saving or listing configuration contacted downloader")
	}
	probe := f.request(t, "POST", path+"/test", nil)
	m5EndpointStatus(t, probe, http.StatusOK)
	if m5EndpointObject(t, probe)["alive"] != true || calls.Load() != 1 {
		t.Fatal("native probe did not execute exactly once")
	}
	input["expected_updated_at"] = row["updated_at"]
	input["password"] = ""
	updated := f.request(t, "PUT", path, input)
	m5EndpointStatus(t, updated, http.StatusOK)
	if !bytes.Equal(ciphertext, f.cipher(t, "qbittorrent", path)) || calls.Load() != 1 {
		t.Fatal("empty secret edit changed credentials or performed network IO")
	}
	m5EndpointStatus(t, f.request(t, "PUT", path, input), http.StatusConflict)
	fail.Store(true)
	probe = f.request(t, "POST", path+"/test", nil)
	m5EndpointStatus(t, probe, http.StatusOK)
	if m5EndpointObject(t, probe)["alive"] != false || calls.Load() != 2 {
		t.Fatal("XML-RPC fault became successful connection")
	}
	f.privateRecords(t)
	var jobs, tasks int
	if err := f.app.DBPool().QueryRow(t.Context(), `SELECT (SELECT count(*) FROM job),(SELECT count(*) FROM download_task)`).Scan(&jobs, &tasks); err != nil || jobs != 0 || tasks != 0 {
		t.Fatal("configuration or probe enqueued a download", err)
	}
	f.exec(t, `UPDATE app_user SET role='member' WHERE username='endpoint-admin'`)
	m5EndpointStatus(t, f.request(t, "POST", path+"/test", nil), http.StatusForbidden)
	// Members retain the safe selection list; management and network probes remain forbidden.
	m5EndpointStatus(t, f.request(t, "GET", "/api/v1/download-clients", nil), http.StatusOK)
	m5EndpointStatus(t, f.request(t, "PUT", path, input), http.StatusForbidden)
	if calls.Load() != 2 {
		t.Fatal("revoked account reached native endpoint")
	}
	t.Log("M11_RTORRENT_COOKIE_PASS Cookie=REAL configuration=ENCRYPTED save=ZERO_IO probe=NATIVE_XML_RPC fault=NOT_SUCCESS permission=LIVE jobs=ZERO")
}
