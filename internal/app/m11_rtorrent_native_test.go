package app

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type m11RTorrentMovieProtocol struct {
	t                   *testing.T
	f                   *organizationFixture
	torrent             []byte
	hash                string
	total               int64
	adds                *atomic.Int32
	reads, unknownReads atomic.Int32
	lost                atomic.Bool
	mu                  *sync.Mutex
	save, tags          *string
}

func (p *m11RTorrentMovieProtocol) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, password, authenticated := r.BasicAuth()
	if r.Method != http.MethodPost || !authenticated || user != "fixture" || password != "fixture-private-password" || r.Header.Get("Content-Type") != "text/xml" {
		p.t.Error("rTorrent native authentication or transport changed")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var request struct {
		Method string `xml:"methodName"`
		Params []struct {
			Value struct {
				Text   string `xml:"string"`
				Binary string `xml:"base64"`
			} `xml:"value"`
		} `xml:"params>param"`
	}
	if err := xml.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&request); err != nil {
		p.t.Error("invalid owned RPC request", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	reply := func(value any) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, "<methodResponse><params><param>"+m11RTorrentXML(value)+"</param></params></methodResponse>")
	}
	switch request.Method {
	case "system.client_version":
		reply("0.9.8")
	case "load.raw_start":
		if len(request.Params) != 4 || request.Params[0].Value.Text != "" {
			p.t.Error("rTorrent missing load parameters")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(request.Params[1].Value.Binary)
		var operation uuid.UUID
		var directory string
		bindingErr := p.f.pool.QueryRow(r.Context(), `SELECT t.operation_id,t.save_path FROM download_task t JOIN download_task_client_binding b ON b.task_id=t.id WHERE t.info_hash=$1 AND t.client_id=$2 AND t.state='submitting'`, p.hash, p.f.client).Scan(&operation, &directory)
		tags := "pulsemedia,pulsemedia:op:" + operation.String()
		if err != nil || !bytes.Equal(raw, p.torrent) || bindingErr != nil || directory == "" || request.Params[2].Value.Text != `d.directory.set="`+directory+`"` || request.Params[3].Value.Text != `d.custom.set=pulsemedia.tags,"`+tags+`"` {
			p.t.Error("rTorrent changed original bytes, path, tags or persisted binding", bindingErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		*p.save, *p.tags = directory, tags
		p.mu.Unlock()
		if p.adds.Add(1) != 1 {
			p.t.Error("rTorrent Add replayed")
			w.WriteHeader(http.StatusConflict)
			return
		}
		if p.lost.Load() {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				p.t.Error("owned receipt loss", err)
				return
			}
			_ = connection.Close()
			return
		}
		reply(int64(0))
	case "d.multicall2":
		expected := []string{"", "main", "d.hash=", "d.name=", "d.directory=", "d.base_path=", "d.is_multi_file=", "d.size_bytes=", "d.completed_bytes=", "d.up.total=", "d.complete=", "d.is_active=", "d.hashing=", "d.is_hash_checking=", "d.custom=pulsemedia.tags", "d.down.rate="}
		if len(request.Params) != len(expected) {
			p.t.Error("rTorrent missing observation fields")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for i, value := range expected {
			if request.Params[i].Value.Text != value {
				p.t.Error("rTorrent observation method changed")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		p.reads.Add(1)
		var unknown bool
		if err := p.f.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM download_task WHERE client_id=$1 AND state='unknown')`, p.f.client).Scan(&unknown); err != nil {
			p.t.Error("read persisted recovery state", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if unknown {
			p.unknownReads.Add(1)
		}
		p.mu.Lock()
		directory, tags := *p.save, *p.tags
		p.mu.Unlock()
		rows := []any{}
		if directory != "" {
			rows = append(rows, []any{strings.ToUpper(p.hash), "Movie.2020.mkv", directory, filepath.Join(directory, "Movie.2020.mkv"), int64(0), p.total, p.total, int64(0), int64(1), int64(1), int64(0), int64(0), tags, int64(0)})
		}
		reply(rows)
	default:
		p.t.Error("unexpected rTorrent mutation or method", request.Method)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func m11RTorrentXML(value any) string {
	switch v := value.(type) {
	case string:
		var escaped bytes.Buffer
		_ = xml.EscapeText(&escaped, []byte(v))
		return "<value><string>" + escaped.String() + "</string></value>"
	case int64:
		return fmt.Sprintf("<value><i8>%d</i8></value>", v)
	case []any:
		var result strings.Builder
		result.WriteString("<value><array><data>")
		for _, item := range v {
			result.WriteString(m11RTorrentXML(item))
		}
		result.WriteString("</data></array></value>")
		return result.String()
	default:
		panic("unsupported owned RPC fixture value")
	}
}

func TestM11RTorrentMovieNativePG(t *testing.T)              { runM11RTorrentMovieNative(t, false) }
func TestM11RTorrentMovieLostReceiptRecoveryPG(t *testing.T) { runM11RTorrentMovieNative(t, true) }

func runM11RTorrentMovieNative(t *testing.T, lost bool) {
	t.Helper()
	runFullMoviePipelineConfigured(t, false, "", "", false, true, func(f *organizationFixture, _ *ports.Actor, _ uuid.UUID, _ *App) func() {
		if err := os.Link(filepath.Join(f.target, "source.mkv"), filepath.Join(f.target, "Movie.2020.mkv")); err != nil {
			t.Fatal("prepare native file name", err)
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE download_client SET client_type='rtorrent',updated_at=clock_timestamp() WHERE id=$1`, f.client); err != nil {
			t.Fatal("configure native rTorrent", err)
		}
		f.rtorrentProbe.lost.Store(lost)
		return func() {
			task, _ := waitSeedingOrganization(t, f)
			var kind, state, organization string
			var binding int
			err := f.pool.QueryRow(t.Context(), `SELECT c.client_type,t.state,t.organization_state,(SELECT count(*) FROM download_task_client_binding b WHERE b.task_id=t.id) FROM download_task t JOIN download_client c ON c.id=t.client_id WHERE t.id=$1`, task).Scan(&kind, &state, &organization, &binding)
			if err != nil || kind != "rtorrent" || state != "completed" || organization != "completed" || binding != 1 || f.rtorrentProbe.reads.Load() == 0 || (lost && f.rtorrentProbe.unknownReads.Load() == 0) {
				t.Fatal("native ingestion or read-only recovery evidence missing", kind, state, organization, binding, err)
			}
			t.Logf("M11_RTORRENT_MOVIE_APP_PASS lost_receipt=%t App=PRODUCTION RPC=OWNED original_bytes=EXACT binding=PERSISTED Add=ONCE recovery=READ_ONLY routing=FROZEN import=REAL_HARDLINK", lost)
		}
	})
}
