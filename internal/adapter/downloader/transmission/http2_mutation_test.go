package transmission

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Receive the whole mutation before returning a retryable HTTP/2 reset. A
// transparent replay would bypass the caller's unknown-outcome reconciliation.
func TestTransmissionHTTP2DoesNotReplayReceivedMutation(t *testing.T) {
	for _, operation := range []string{"add", "remove"} {
		t.Run(operation, func(t *testing.T) {
			var received atomic.Int32
			peerErrors := make(chan error, 1)
			server := httptest.NewUnstartedServer(nil)
			server.EnableHTTP2 = true
			server.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
				"h2": func(_ *http.Server, conn *tls.Conn, _ http.Handler) {
					if err := serveMutationResetPeer(conn, &received); err != nil {
						select {
						case peerErrors <- err:
						default:
						}
					}
				},
			}
			server.StartTLS()
			defer func() {
				server.Client().CloseIdleConnections()
				server.CloseClientConnections()
				server.Close()
				select {
				case err := <-peerErrors:
					t.Errorf("HTTP/2 peer: %v", err)
				default:
				}
			}()
			client, err := NewClient(server.URL, "", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			hash := strings.Repeat("a", 40)
			if operation == "add" {
				err = client.AddTorrent(ctx, ports.AddTorrentParam{TorrentBytes: []byte("inspected-metainfo"), InfoHash: hash})
			} else {
				err = client.RemoveTorrent(ctx, hash, false)
			}
			if received.Load() != 1 || !errors.Is(err, ports.ErrUnavailable) {
				t.Fatalf("received mutation streams=%d error=%v; must not replay after receipt", received.Load(), err)
			}
		})
	}
}

func serveMutationResetPeer(conn *tls.Conn, received *atomic.Int32) error {
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		return err
	}
	preface := make([]byte, 24)
	if _, err := io.ReadFull(conn, preface); err != nil {
		return err
	}
	if string(preface) != "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" {
		return errors.New("missing HTTP/2 preface")
	}
	if err := writeMutationFrame(conn, 4, 0, 0, nil); err != nil {
		return err
	}
	for {
		var header [9]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		length := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
		if length > 1<<20 {
			return errors.New("oversized HTTP/2 frame")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return err
		}
		stream := binary.BigEndian.Uint32(header[5:]) & 0x7fffffff
		switch header[3] {
		case 4, 6: // SETTINGS or PING
			if header[4]&1 == 0 {
				var acknowledgement []byte
				if header[3] == 6 {
					acknowledgement = payload
				}
				if err := writeMutationFrame(conn, header[3], 1, 0, acknowledgement); err != nil {
					return err
				}
			}
		case 0: // DATA with END_STREAM: all request bytes have been received.
			if header[4]&1 == 0 {
				continue
			}
			if received.Add(1) == 1 {
				if err := writeMutationFrame(conn, 3, 0, stream, []byte{0, 0, 0, 1}); err != nil {
					return err
				}
			} else {
				if err := writeMutationFrame(conn, 1, 4, stream, []byte{0x88}); err != nil {
					return err
				}
				if err := writeMutationFrame(conn, 0, 1, stream, []byte(`{"result":"success","arguments":{}}`)); err != nil {
					return err
				}
			}
		}
	}
}

func writeMutationFrame(w io.Writer, kind, flags byte, stream uint32, payload []byte) error {
	header := []byte{byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload)), kind, flags, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[5:], stream)
	_, err := w.Write(append(header, payload...))
	return err
}

func TestTransmissionAddExplicitSessionNegotiation(t *testing.T) {
	var requests, accepted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Transmission-Session-Id") != "fixture-session" {
			w.Header().Set("X-Transmission-Session-Id", "fixture-session")
			w.WriteHeader(http.StatusConflict)
			return
		}
		accepted.Add(1)
		_, _ = io.WriteString(w, `{"result":"success","arguments":{}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.AddTorrent(t.Context(), ports.AddTorrentParam{TorrentBytes: []byte("inspected-metainfo"), InfoHash: strings.Repeat("a", 40)})
	if err != nil || requests.Load() != 2 || accepted.Load() != 1 {
		t.Fatalf("session negotiation requests=%d mutations=%d error=%v", requests.Load(), accepted.Load(), err)
	}
}
