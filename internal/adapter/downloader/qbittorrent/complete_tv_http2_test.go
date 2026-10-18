package qbittorrent

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
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

// A minimal TLS HTTP/2 peer acknowledges settings and fully receives Add before
// returning PROTOCOL_ERROR. A second stream would mean the mutation was replayed.
func TestCompleteTVByteSenderHTTP2DoesNotReplayReceivedAdd(t *testing.T) {
	testHTTP2ReceivedAdd(t, true)
}
func TestOrdinarySenderHTTP2DoesNotReplayReceivedAdd(t *testing.T) { testHTTP2ReceivedAdd(t, false) }

func testHTTP2ReceivedAdd(t *testing.T, completeTV bool) {
	var received atomic.Int32
	peerErrors := make(chan error, 1)
	s := httptest.NewUnstartedServer(nil)
	s.EnableHTTP2 = true
	s.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){
		"h2": func(_ *http.Server, c *tls.Conn, _ http.Handler) {
			if e := serveCompleteTVResetPeer(c, &received); e != nil {
				select {
				case peerErrors <- e:
				default:
				}
			}
		},
	}
	s.StartTLS()
	defer func() {
		s.Client().CloseIdleConnections()
		s.CloseClientConnections()
		s.Close()
		select {
		case e := <-peerErrors:
			t.Errorf("HTTP/2 peer: %v", e)
		default:
		}
	}()
	client, e := NewClient(s.URL, "unused", "unused", s.Client())
	if e != nil {
		t.Fatal(e)
	}
	client.loggedIn = true // Exercise the actual Add transport without an auth stream.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	add := client.AddTorrent
	if completeTV {
		add = client.CompleteTVByteSender().AddTorrent
	}
	e = add(ctx, ports.AddTorrentParam{
		TorrentBytes: []byte("the-exact-inspected-torrent-bytes"),
		InfoHash:     strings.Repeat("a", 40), SavePath: "/frozen/root", Tags: "pulsemedia,pulsemedia:op:fixed",
	})
	if received.Load() != 1 || !errors.Is(e, ports.ErrUnavailable) {
		t.Fatalf("received Add streams=%d, error=%v; reset after receipt must not resend", received.Load(), e)
	}
}

func serveCompleteTVResetPeer(c *tls.Conn, received *atomic.Int32) error {
	if e := c.SetDeadline(time.Now().Add(8 * time.Second)); e != nil {
		return e
	}
	preface := make([]byte, 24)
	if _, e := io.ReadFull(c, preface); e != nil {
		return e
	}
	if string(preface) != "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" {
		return errors.New("missing HTTP/2 preface")
	}
	if e := writeCompleteTVFrame(c, 4, 0, 0, nil); e != nil {
		return e
	}
	for {
		var header [9]byte
		if _, e := io.ReadFull(c, header[:]); e != nil {
			// Test cleanup closes the listener-side connection while this peer
			// waits for a possible replay. Both close outcomes are normal.
			if errors.Is(e, io.EOF) || errors.Is(e, net.ErrClosed) {
				return nil
			}
			return e
		}
		n := int(header[0])<<16 | int(header[1])<<8 | int(header[2])
		if n > 1<<20 {
			return errors.New("oversized HTTP/2 frame")
		}
		payload := make([]byte, n)
		if _, e := io.ReadFull(c, payload); e != nil {
			return e
		}
		stream := binary.BigEndian.Uint32(header[5:]) & 0x7fffffff
		switch header[3] {
		case 4: // SETTINGS
			if header[4]&1 == 0 {
				if e := writeCompleteTVFrame(c, 4, 1, 0, nil); e != nil {
					return e
				}
			}
		case 6: // PING
			if header[4]&1 == 0 {
				if e := writeCompleteTVFrame(c, 6, 1, 0, payload); e != nil {
					return e
				}
			}
		case 0: // DATA
			if header[4]&1 == 0 {
				continue
			}
			if received.Add(1) == 1 {
				// The remote side already accepted the full mutation before the reset.
				if e := writeCompleteTVFrame(c, 3, 0, stream, []byte{0, 0, 0, 1}); e != nil {
					return e
				}
			} else {
				// An unsafe transport replay gets an affirmative response, demonstrating
				// that one Do call can otherwise conceal two received Add requests.
				if e := writeCompleteTVFrame(c, 1, 4, stream, []byte{0x88}); e != nil {
					return e
				}
				if e := writeCompleteTVFrame(c, 0, 1, stream, []byte("Ok.")); e != nil {
					return e
				}
			}
		}
	}
}
func writeCompleteTVFrame(w io.Writer, kind, flags byte, stream uint32, payload []byte) error {
	if len(payload) > 0xffffff {
		return fmt.Errorf("frame too large: %d", len(payload))
	}
	header := []byte{byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload)), kind, flags, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[5:], stream)
	_, e := w.Write(append(header, payload...))
	return e
}
