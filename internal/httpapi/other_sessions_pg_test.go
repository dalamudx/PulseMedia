package httpapi_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

func TestRevokeOtherSessionsActualRouterPG(t *testing.T) {
	if strings.TrimSpace(os.Getenv("PULSE_TEST_DATABASE_URL")) != "" {
		t.Setenv("PULSE_ACCOUNT_SECURITY_ACCEPTANCE", "1")
	}
	a, _, _ := setupAuthHTTP(t)
	ctx := t.Context()
	c := a.Clock().(*clock.MockClock)
	secrets := crypto.AuthenticationSecrets{}
	cipher := crypto.NewSettingsCipher(make([]byte, 32))
	login := auth.NewService(postgres.NewAuthenticationStore(a.DBPool()), secrets, c, a.IDGen())
	login.SetMFACipher(cipher)
	const password = "OtherSessionsFixture123!"
	admin, adminRaw, err := login.SetupAdmin(ctx, "OtherSessionsAdmin", password)
	if err != nil {
		t.Fatal(err)
	}
	adminActor, err := login.AuthenticateToken(ctx, adminRaw)
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.NewAccountManagementStore(a.DBPool(), a.IDGen())
	accounts := auth.NewAccountManagementService(store, secrets, c, a.IDGen(), cipher)
	mfa := auth.NewMFAService(store, secrets, cipher, c, a.IDGen())
	created, err := accounts.Create(ctx, adminActor, adminRaw, ports.AccountCreate{Username: "OtherSessionsMember", Password: password, Role: ports.RoleMember}, "fixture-member")
	if err != nil || created.User == nil {
		t.Fatal("create member", err)
	}
	member, memberRaw, err := login.Login(ctx, created.User.Username, password)
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewServerHandler(a, login, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetAccountManagementService(accounts)
	h.SetMFAService(mfa)
	router := httpapi.NewRouter(a, h, login, nil)
	const path = "/api/v1/auth/sessions/revoke-others"
	const action = "account.sessions.others_revoked"
	body := `{"current_password":"` + password + `"}`
	send := func(target http.Handler, method, endpoint, key, input, raw string, advance bool, alter func(*http.Request)) *httptest.ResponseRecorder {
		if advance {
			c.Advance(6 * time.Second)
		}
		r := httptest.NewRequest(method, "http://example.com"+endpoint, strings.NewReader(input))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://example.com")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if raw != "" {
			r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: raw})
		}
		if alter != nil {
			alter(r)
		}
		w := httptest.NewRecorder()
		target.ServeHTTP(w, r)
		return w
	}
	check := func(t *testing.T, w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("cacheable security response")
		}
		for _, secret := range []string{password, adminRaw, memberRaw} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("response exposed credential")
			}
		}
	}
	post := func(raw, key, input string) *httptest.ResponseRecorder {
		return send(router, "POST", path, key, input, raw, true, nil)
	}
	alive := func(t *testing.T, raw string, want bool) {
		t.Helper()
		_, e := login.AuthenticateToken(ctx, raw)
		if (e == nil) != want {
			t.Fatalf("credential validity=%v want=%v", e == nil, want)
		}
	}
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, c)
	_, token, err := tokens.Create(ctx, adminActor, ports.TokenCommand{Name: "other sessions token"}, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("cookie bearer token CSRF", func(t *testing.T) {
		for _, tc := range []struct {
			name, raw string
			status    int
			alter     func(*http.Request)
		}{
			{"anonymous", "", 401, nil},
			{"invalid cookie", "invalid-session", 401, nil},
			{"session bearer", "", 403, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+adminRaw) }},
			{"token bearer", "", 403, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }},
			{"token cookie", token, 403, nil},
			{"cookie and bearer", adminRaw, 403, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+adminRaw) }},
			{"cross origin", adminRaw, 403, func(r *http.Request) { r.Header.Set("Origin", "https://foreign.example") }},
			{"missing origin", adminRaw, 403, func(r *http.Request) { r.Header.Del("Origin") }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				check(t, send(router, "POST", path, "denied", body, tc.raw, true, tc.alter), tc.status)
			})
		}
	})
	t.Run("strict JSON and headers", func(t *testing.T) {
		for _, tc := range []struct{ name, body string }{
			{"duplicate password", `{"current_password":"x","current_password":"y"}`},
			{"escaped duplicate", `{"current_password":"x","current\u005fpassword":"y"}`},
			{"case alias", `{"Current_Password":"x"}`},
			{"factor case", `{"current_password":"x","OTP":"123456"}`},
			{"duplicate factor", `{"current_password":"x","otp":"123456","otp":"654321"}`},
			{"null password", `{"current_password":null}`},
			{"null otp", `{"current_password":"x","otp":null}`},
			{"null recovery", `{"current_password":"x","recovery_code":null}`},
			{"numeric factor", `{"current_password":"x","otp":123456}`},
			{"target override", `{"current_password":"x","user_id":"` + member.ID.String() + `"}`},
			{"session override", `{"current_password":"x","session_id":"` + admin.ID.String() + `"}`},
			{"missing password", `{}`}, {"null body", `null`}, {"array", `[]`},
			{"trailing body", body + ` {}`}, {"truncated", `{"current_password":`},
			{"oversized", `{"current_password":"` + strings.Repeat("x", 8193) + `"}`},
		} {
			t.Run(tc.name, func(t *testing.T) { check(t, post(adminRaw, "strict", tc.body), 400) })
		}
		for _, tc := range []struct {
			name  string
			alter func(*http.Request)
		}{
			{"missing key", func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
			{"duplicate key", func(r *http.Request) { r.Header.Add("Idempotency-Key", "second") }},
			{"combined key", func(r *http.Request) { r.Header.Set("Idempotency-Key", "one,two") }},
			{"whitespace key", func(r *http.Request) { r.Header.Set("Idempotency-Key", " padded ") }},
			{"long key", func(r *http.Request) { r.Header.Set("Idempotency-Key", strings.Repeat("x", 161)) }},
			{"duplicate cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: adminRaw}) }},
			{"missing content type", func(r *http.Request) { r.Header.Del("Content-Type") }},
			{"wrong content type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
			{"duplicate content type", func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				check(t, send(router, "POST", path, "strict", body, adminRaw, true, tc.alter), 400)
			})
		}
	})
	for _, tc := range []struct{ role, name, raw, foreign string }{
		{"member", member.Username, memberRaw, adminRaw}, {"admin", admin.Username, adminRaw, memberRaw},
	} {
		t.Run(tc.role+" own sessions and replay", func(t *testing.T) {
			_, other, e := login.Login(ctx, tc.name, password)
			if e != nil {
				t.Fatal(e)
			}
			actor, e := login.AuthenticateToken(ctx, tc.raw)
			if e != nil {
				t.Fatal(e)
			}
			sessions, e := accounts.Sessions(ctx, actor, tc.raw)
			if e != nil {
				t.Fatal(e)
			}
			var current ports.ManagedSession
			for _, s := range sessions {
				if s.Current {
					current = s
				}
			}
			if !current.Current {
				t.Fatal("current session not identified")
			}
			w := post(tc.raw, tc.role+"-wrong", `{"current_password":"wrong"}`)
			check(t, w, 400)
			if !strings.Contains(w.Body.String(), "REAUTHENTICATION_FAILED") {
				t.Fatal("wrong reauth error")
			}
			alive(t, other, true)
			key := tc.role + "-revoke"
			w = post(tc.raw, key, body)
			check(t, w, 200)
			var result ports.AccountCommandResult
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.SessionID == nil || *result.SessionID != current.ID || result.Replayed || result.User != nil {
				t.Fatal("wrong kept-current result")
			}
			alive(t, tc.raw, true)
			alive(t, other, false)
			alive(t, tc.foreign, true)
			alive(t, token, true)
			_, later, e := login.Login(ctx, tc.name, password)
			if e != nil {
				t.Fatal(e)
			}
			w = send(router, "GET", "/api/v1/auth/security/commands/"+key, "", "", tc.raw, true, nil)
			check(t, w, 200)
			var receipt ports.SecurityCommandReceipt
			if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Action != action {
				t.Fatal("wrong receipt action")
			}
			var kept ports.AccountCommandResult
			if json.Unmarshal(receipt.Result, &kept) != nil || kept.SessionID == nil || *kept.SessionID != current.ID {
				t.Fatal("receipt lost kept session")
			}
			check(t, send(router, "GET", "/api/v1/auth/security/commands/"+key, "", "", tc.foreign, true, nil), 404)
			c.Advance(time.Minute)
			responses := make(chan *httptest.ResponseRecorder, 2)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); responses <- send(router, "POST", path, key, body, tc.raw, false, nil) }()
			}
			wg.Wait()
			close(responses)
			for w := range responses {
				check(t, w, 200)
				var replay ports.AccountCommandResult
				if json.Unmarshal(w.Body.Bytes(), &replay) != nil || !replay.Replayed || replay.SessionID == nil || *replay.SessionID != current.ID {
					t.Fatal("unsafe concurrent replay")
				}
			}
			check(t, post(tc.raw, key, `{"current_password":"changed"}`), 409)
			alive(t, later, true)
			alive(t, tc.raw, true)
			alive(t, tc.foreign, true)
			alive(t, token, true)
			check(t, send(router, "GET", "/api/v1/auth/sessions", "", "", other, true, nil), 401)
		})
	}
	t.Run("MFA reauthentication OTP recovery and replay", func(t *testing.T) {
		actor, e := login.AuthenticateToken(ctx, adminRaw)
		if e != nil {
			t.Fatal(e)
		}
		setup, e := mfa.Begin(ctx, actor, adminRaw, password, "mfa-begin")
		if e != nil {
			t.Fatal(e)
		}
		otp := func() string {
			secret, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
			if e != nil {
				t.Fatal(e)
			}
			var counter [8]byte
			binary.BigEndian.PutUint64(counter[:], uint64(c.Now().Unix()/30))
			mac := hmac.New(sha1.New, secret)
			_, _ = mac.Write(counter[:])
			sum := mac.Sum(nil)
			offset := sum[len(sum)-1] & 15
			return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
		}
		confirmed, e := mfa.Confirm(ctx, actor, adminRaw, setup.ChallengeID, otp(), "mfa-confirm")
		if e != nil || len(confirmed.RecoveryCodes) != 10 {
			t.Fatal("MFA fixture", e)
		}
		// Enabling MFA revokes existing tokens; exercise a fresh token throughout this command.
		_, token, e := tokens.Create(ctx, actor, ports.TokenCommand{Name: "MFA other sessions token"}, "mfa-fixture-token")
		if e != nil {
			t.Fatal(e)
		}
		_, other, e := login.Login(ctx, admin.Username, password, ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[0]})
		if e != nil {
			t.Fatal(e)
		}
		for _, input := range []string{body, `{"current_password":"wrong","recovery_code":"` + confirmed.RecoveryCodes[1] + `"}`, body[:len(body)-1] + `,"recovery_code":"INVALID-RECOVERY"}`, body[:len(body)-1] + `,"otp":"123456","recovery_code":"x"}`} {
			check(t, post(adminRaw, "mfa-denied", input), 400)
			alive(t, other, true)
		}
		recoveryBody := body[:len(body)-1] + `,"recovery_code":"` + confirmed.RecoveryCodes[1] + `"}`
		check(t, post(adminRaw, "mfa-recovery", recoveryBody), 200)
		alive(t, other, false)
		alive(t, adminRaw, true)
		alive(t, token, true)
		alive(t, memberRaw, true)
		_, later, e := login.Login(ctx, admin.Username, password, ports.MFAProof{RecoveryCode: confirmed.RecoveryCodes[2]})
		if e != nil {
			t.Fatal(e)
		}
		check(t, post(adminRaw, "mfa-recovery", recoveryBody), 200)
		alive(t, later, true)
		check(t, post(adminRaw, "used-recovery", recoveryBody), 400)
		alive(t, later, true)
		c.Advance(time.Minute)
		otpBody := body[:len(body)-1] + `,"otp":"` + otp() + `"}`
		check(t, post(adminRaw, "mfa-otp", otpBody), 200)
		alive(t, later, false)
		alive(t, adminRaw, true)
		alive(t, token, true)
		check(t, post(adminRaw, "used-otp", otpBody), 400)
		w := send(router, "GET", "/api/v1/auth/security/commands/mfa-recovery", "", "", adminRaw, true, nil)
		check(t, w, 200)
		for _, secret := range append([]string{setup.Secret}, confirmed.RecoveryCodes...) {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("receipt exposed MFA secret")
			}
		}
	})
	t.Run("one receipt audit and event per command", func(t *testing.T) {
		var receipts, audits, events int
		e := a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM account_security_command WHERE action=$1),(SELECT count(*) FROM audit_log WHERE action=$1),(SELECT count(*) FROM outbox_event WHERE event_type=$1)`, action).Scan(&receipts, &audits, &events)
		if e != nil || receipts != 4 || audits != 4 || events != 4 {
			t.Fatal("duplicate or missing effects", e, receipts, audits, events)
		}
	})
	t.Run("finite shared security budget includes new path", func(t *testing.T) {
		limited := httpapi.NewRouter(a, h, login, nil)
		for i := 0; i < 10; i++ {
			check(t, send(limited, "POST", path, "rate", `{}`, memberRaw, false, nil), 400)
		}
		for _, endpoint := range []string{path, "/api/v1/auth/password", "/api/v1/auth/mfa/recovery-codes"} {
			w := send(limited, "POST", endpoint, "rate", `{}`, memberRaw, false, nil)
			check(t, w, 429)
			if w.Header().Get("Retry-After") != "6" || !strings.Contains(w.Body.String(), "AUTH_RATE_LIMITED") {
				t.Fatal("missing rate-limit evidence")
			}
		}
		c.Advance(6 * time.Second)
		check(t, send(limited, "POST", path, "rate", `{}`, memberRaw, false, nil), 400)
		check(t, send(limited, "POST", path, "rate", `{}`, memberRaw, false, nil), 429)
		check(t, send(limited, "GET", "/api/v1/auth/security/commands/member-revoke", "", "", memberRaw, false, nil), 200)
	})
}
