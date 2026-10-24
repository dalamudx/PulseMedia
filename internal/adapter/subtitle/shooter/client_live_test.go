package shooter

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net/http/httptrace"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

// Opt-in real upstream acceptance; the formal wrapper computes the fixed public
// CC BY media hash from four verified HTTP byte ranges, never local private media.
func TestShooterPublicSintelLive(t *testing.T) {
	if os.Getenv("PULSE_M8_SHOOTER_LIVE_ACCEPTANCE") != "1" {
		t.Skip("explicit real upstream acceptance required")
	}
	hash := ports.SubtitleVideoHash{Hash: os.Getenv("PULSE_M8_SHOOTER_LIVE_HASH"), SizeBytes: 1180090590}
	verifyShooterLiveSRT(t, hash, "Sintel_CC_BY_range_reads", true)
}

// This public protocol fixture contains no film bytes. It tests the real service
// independently of the licensed Sintel sample, whose version currently has no match.
func TestShooterPublishedProtocolLive(t *testing.T) {
	if os.Getenv("PULSE_M8_SHOOTER_PROTOCOL_ACCEPTANCE") != "1" {
		t.Skip("explicit published protocol sample acceptance required")
	}
	// Subliminal 2.1.0 tests/conftest.py and tests/test_shooter.py.
	hash := ports.SubtitleVideoHash{Hash: "314f454ab464775498ae6f1f5ad813a9;fdaa8b702d8936feba2122e93ba5c44f;0a6935e3436aa7db5597ef67a2c494e3;4d269733f36ddd49f71e92732a462fe5", SizeBytes: 7033732714}
	verifyShooterLiveSRT(t, hash, "Subliminal_2_1_0_published_protocol_hash", false)
}

func verifyShooterLiveSRT(t *testing.T, hash ports.SubtitleVideoHash, origin string, physicalRead bool) {
	t.Helper()
	if !media.ValidShooterHash(hash.Hash, hash.SizeBytes) {
		t.Fatal("invalid public media hash")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	// Only event bits are recorded; trace callbacks never retain hosts, addresses,
	// headers, request URLs or native errors that may contain signed links.
	var events atomic.Uint32
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { events.Or(1) },
		DNSDone:              func(httptrace.DNSDoneInfo) { events.Or(2) },
		ConnectStart:         func(string, string) { events.Or(4) },
		ConnectDone:          func(string, string, error) { events.Or(8) },
		TLSHandshakeStart:    func() { events.Or(16) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { events.Or(32) },
		GotConn:              func(httptrace.GotConnInfo) { events.Or(64) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { events.Or(128) },
		GotFirstResponseByte: func() { events.Or(256) },
	})
	started := time.Now()
	body, err := NewClient("", nil).FindChineseSubtitle(ctx, hash)
	if err != nil {
		code := "UPSTREAM_REJECTED"
		switch {
		case errors.Is(err, ports.ErrTimeout):
			code = "TIMEOUT"
		case errors.Is(err, ports.ErrUnavailable):
			code = "UNAVAILABLE"
		case errors.Is(err, ports.ErrBadResponse):
			code = "BAD_RESPONSE"
		}
		t.Fatalf("M8_LIVE_SHOOTER_UNVERIFIED outcome=%s trace_events=%d elapsed_ms=%d", code, events.Load(), time.Since(started).Milliseconds())
	}
	if len(body) == 0 {
		t.Fatal("M8_LIVE_SHOOTER_UNVERIFIED outcome=NO_MATCH")
	}
	normalized, err := media.NormalizeChineseSRT(body)
	if err != nil || !bytes.Equal(normalized, body) {
		t.Fatal("M8_LIVE_SHOOTER_UNVERIFIED outcome=INVALID_SRT")
	}
	t.Logf("M8_LIVE_SHOOTER_PASS native_client=true TLS_verified=true hash_origin=%s physical_source_read=%t SRT_bytes=%d retries=0 fixture=false", origin, physicalRead, len(body))
}
