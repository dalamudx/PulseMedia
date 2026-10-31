package transmission

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeControlTransmissionExactHashAndSession(t *testing.T) {
	var mutations, accepted atomic.Int32
	hash := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method    string `json:"method"`
			Arguments struct {
				IDs []string `json:"ids"`
			} `json:"arguments"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("body")
		}
		if len(request.Arguments.IDs) != 1 || request.Arguments.IDs[0] != hash {
			t.Error("not exact hash", request.Arguments.IDs)
		}
		if request.Method == "torrent-get" {
			_, _ = fmt.Fprintf(w, `{"result":"success","tag":1,"arguments":{"torrents":[{"hashString":%q,"downloadDir":"/downloads","status":0,"labels":["pulsemedia"]}]}}`, hash)
			return
		}
		mutations.Add(1)
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", "session")
			w.WriteHeader(409)
			return
		}
		if request.Method != "torrent-stop" && request.Method != "torrent-start" {
			t.Error(request.Method)
		}
		accepted.Add(1)
		_, _ = fmt.Fprint(w, `{"result":"success","tag":1,"arguments":{}}`)
	}))
	defer server.Close()
	c, _ := NewClient(server.URL, "user", "secret", server.Client())
	for _, paused := range []bool{true, false} {
		if e := c.SetTorrentPaused(t.Context(), strings.ToUpper(hash), paused); e != nil {
			t.Fatal(e)
		}
	}
	if accepted.Load() != 2 || mutations.Load() != 3 {
		t.Fatal(accepted.Load(), mutations.Load())
	}
	observed, e := c.ReadTorrentPaused(t.Context(), hash)
	if e != nil || !observed.Paused || observed.Info.InfoHash != hash {
		t.Fatal(observed, e)
	}
}
func TestNativeControlTransmissionRejectsAmbiguityAndRedirect(t *testing.T) {
	for _, body := range []string{`{"result":"success","result":"success"}`, `{"result":"success","Tag":2}`, `{"result":"success","tag":2}`, `{"result":"success","tag":null}`, `{"result":null}`, `{"result":"success"} {}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) }))
			defer server.Close()
			c, _ := NewClient(server.URL, "", "", server.Client())
			if e := c.SetTorrentPaused(t.Context(), strings.Repeat("a", 40), true); e == nil {
				t.Fatal("ambiguous ACK accepted")
			}
		})
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(307)
	}))
	defer origin.Close()
	c, _ := NewClient(origin.URL, "user", "secret", origin.Client())
	if e := c.SetTorrentPaused(t.Context(), strings.Repeat("a", 40), true); e == nil || redirected.Load() != 0 {
		t.Fatal("redirect followed", e)
	}
}
