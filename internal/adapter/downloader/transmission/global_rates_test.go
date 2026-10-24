package transmission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const globalRatesQB = false

func TestGlobalTransferRatesNative(t *testing.T) {
	for _, tc := range []struct {
		name, down, up   string
		wantDown, wantUp *int64
	}{
		{"both", "123", "456", globalRateValue(123), globalRateValue(456)},
		{"idle", "0", "0", globalRateValue(0), globalRateValue(0)},
		{"missing", "", "", nil, nil},
		{"one_missing", "", "37", nil, globalRateValue(37)},
		{"null", "null", "null", nil, nil},
		{"negative", "-1", "-9", nil, nil},
		{"fraction", "1.5", "0.5", nil, nil},
		{"exponent", "1e3", "2e3", nil, nil},
		{"quoted", `"300"`, `"400"`, nil, nil},
		{"overflow", "9223372036854775808", "9223372036854775808", nil, nil},
		{"max", "9223372036854775807", "9223372036854775807", globalRateValue(9223372036854775807), globalRateValue(9223372036854775807)},
		{"invalid_direction", "false", "99", nil, globalRateValue(99)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			downKey, upKey := "downloadSpeed", "uploadSpeed"
			if globalRatesQB {
				downKey, upKey = "dl_info_speed", "up_info_speed"
			}
			fields := map[string]json.RawMessage{"downloadedBytes": json.RawMessage("999999999"), "uploadedBytes": json.RawMessage("888888888")}
			if tc.down != "" {
				fields[downKey] = json.RawMessage(tc.down)
			}
			if tc.up != "" {
				fields[upKey] = json.RawMessage(tc.up)
			}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if globalRatesLogin(w, r) {
					return
				}
				calls.Add(1)
				if globalRatesQB {
					if r.Method != "GET" || r.URL.Path != "/api/v2/transfer/info" {
						t.Error("not a read-only global transfer request")
					}
					_, _ = w.Write(raw)
					return
				}
				var req struct {
					Method    string                     `json:"method"`
					Arguments map[string]json.RawMessage `json:"arguments"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "session-stats" || len(req.Arguments) != 0 {
					t.Error("not read-only global session stats")
				}
				_, _ = fmt.Fprintf(w, `{"result":"success","arguments":%s}`, raw)
			}))
			defer server.Close()
			c, err := NewClient(server.URL, "", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.ReadGlobalRates(t.Context())
			if err != nil || !reflect.DeepEqual(got, ports.DownloadGlobalRates{DownloadBytesPerSecond: tc.wantDown, UploadBytesPerSecond: tc.wantUp}) || calls.Load() != 1 {
				t.Fatal("wrong global observation", got, err, calls.Load())
			}
		})
	}
}
func globalRateValue(n int64) *int64 { return &n }
func globalRatesLogin(w http.ResponseWriter, r *http.Request) bool {
	if globalRatesQB && r.URL.Path == "/api/v2/auth/login" {
		_, _ = w.Write([]byte("Ok."))
		return true
	}
	return false
}

func TestGlobalTransferRatesFailures(t *testing.T) {
	for _, mode := range []string{"auth", "unavailable", "rate_limit", "redirect", "malformed", "null", "array", "oversize", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			release := make(chan struct{})
			var redirected atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if globalRatesLogin(w, r) {
					return
				}
				if r.URL.Path == "/wrong" {
					redirected.Add(1)
					return
				}
				switch mode {
				case "auth":
					w.WriteHeader(403)
				case "unavailable":
					w.WriteHeader(503)
				case "rate_limit":
					w.WriteHeader(429)
				case "redirect":
					w.Header().Set("Location", "/wrong")
					w.WriteHeader(302)
				case "cancel":
					select {
					case <-r.Context().Done():
					case <-release:
					}
				default:
					raw := "{bad"
					switch mode {
					case "null":
						raw = "null"
					case "array":
						raw = "[]"
					case "oversize":
						raw = `{"padding":"` + strings.Repeat("x", (1<<20)+1) + `"}`
					}
					if !globalRatesQB {
						raw = `{"result":"success","arguments":` + raw + `}`
					}
					_, _ = w.Write([]byte(raw))
				}
			}))
			defer func() { close(release); server.Close() }()
			c, err := NewClient(server.URL, "", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if mode == "cancel" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			}
			got, err := c.ReadGlobalRates(ctx)
			if err == nil || got.DownloadBytesPerSecond != nil || got.UploadBytesPerSecond != nil || redirected.Load() != 0 {
				t.Fatal("failed request exposed rates or followed redirect", got, err)
			}
			if mode == "auth" && !errors.Is(err, ports.ErrAuthRequired) {
				t.Fatal("authentication classification changed", err)
			}
			if mode == "unavailable" && !errors.Is(err, ports.ErrUnavailable) {
				t.Fatal("outage classification changed", err)
			}
			if mode == "cancel" && !errors.Is(err, ports.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("deadline classification changed", err)
			}
		})
	}
}
