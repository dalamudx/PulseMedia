package httpapi_test

import (
	"bytes"
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
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/httpapi"
	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/crypto"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/auth"
)

func TestMFARecoveryRegenerationActualRouterPG(t *testing.T) {
	// Explicit PG CI shards must not depend on historical acceptance flags.
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
	const password = "RecoveryRouterFixture123!"
	user, current, err := login.SetupAdmin(ctx, "RecoveryRouter", password)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := login.AuthenticateToken(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.NewAccountManagementStore(a.DBPool(), a.IDGen())
	mfa := auth.NewMFAService(store, secrets, cipher, c, a.IDGen())
	h := httpapi.NewServerHandler(a, login, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetMFAService(mfa)
	h.SetAccountManagementService(auth.NewAccountManagementService(store, secrets, c, a.IDGen(), cipher))
	router := httpapi.NewRouter(a, h, login, nil)
	const path = "/api/v1/auth/mfa/recovery-codes"
	const action = "account.mfa.recovery.regenerated"
	send := func(t *testing.T, target http.Handler, method, endpoint, key, body, credential, origin string, bearer, advance bool, alter func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		if advance {
			c.Advance(6 * time.Second)
		}
		r := httptest.NewRequest(method, "http://example.com"+endpoint, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if credential != "" {
			if bearer {
				r.Header.Set("Authorization", "Bearer "+credential)
			} else {
				r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: credential})
			}
		}
		if alter != nil {
			alter(r)
		}
		w := httptest.NewRecorder()
		target.ServeHTTP(w, r)
		if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("cacheable security response: %d", w.Code)
		}
		if strings.Contains(w.Body.String(), password) || strings.Contains(w.Body.String(), current) {
			t.Fatal("response leaked credentials")
		}
		return w
	}
	post := func(t *testing.T, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		return send(t, router, http.MethodPost, path, key, body, current, "http://example.com", false, true, nil)
	}
	mustStatus := func(t *testing.T, w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status=%d want=%d response=%s", w.Code, want, w.Body.String())
		}
	}
	bodyFor := func(password, field, factor string) string {
		b, e := json.Marshal(map[string]string{"current_password": password, field: factor})
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	t.Run("disabled MFA", func(t *testing.T) {
		mustStatus(t, post(t, "disabled", bodyFor(password, "otp", "123456")), http.StatusConflict)
	})
	setup, err := mfa.Begin(ctx, actor, current, password, "fixture-mfa-setup")
	if err != nil {
		t.Fatal(err)
	}
	// Use the same RFC 6238 calculation as the existing PostgreSQL MFA fixture.
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
	confirmed, err := mfa.Confirm(ctx, actor, current, setup.ChallengeID, otp(), "fixture-mfa-confirm")
	if err != nil || len(confirmed.RecoveryCodes) != 10 {
		t.Fatal("MFA fixture confirmation", err)
	}
	oldCodes := confirmed.RecoveryCodes
	_, other, err := login.Login(ctx, user.Username, password, ports.MFAProof{RecoveryCode: oldCodes[0]})
	if err != nil {
		t.Fatal(err)
	}
	tokens := auth.NewTokenManagementService(postgres.NewTokenManagementStore(a.DBPool(), a.IDGen()), crypto.ReadTokenIssuer{}, c)
	_, token, err := tokens.Create(ctx, actor, ports.TokenCommand{Name: "recovery fixture"}, "fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	body := bodyFor(password, "recovery_code", oldCodes[1])
	t.Run("cookie CSRF and credential admission", func(t *testing.T) {
		for _, tc := range []struct {
			name, credential, origin string
			bearer                   bool
			status                   int
		}{
			{"anonymous", "", "http://example.com", false, http.StatusUnauthorized},
			{"invalid session", "invalid-session", "http://example.com", false, http.StatusUnauthorized},
			{"session bearer", current, "http://example.com", true, http.StatusForbidden},
			{"read token bearer", token, "http://example.com", true, http.StatusForbidden},
			{"read token cookie", token, "http://example.com", false, http.StatusForbidden},
			{"cross origin", current, "https://foreign.example", false, http.StatusForbidden},
			{"missing origin", current, "", false, http.StatusForbidden},
		} {
			t.Run(tc.name, func(t *testing.T) {
				mustStatus(t, send(t, router, http.MethodPost, path, "denied", body, tc.credential, tc.origin, tc.bearer, true, nil), tc.status)
			})
		}
	})
	t.Run("strict JSON and headers", func(t *testing.T) {
		for _, tc := range []struct{ name, body string }{
			{"duplicate", `{"current_password":"x","current_password":"y"}`},
			{"escaped duplicate", `{"current_password":"x","current\u005fpassword":"y"}`},
			{"factor duplicate", `{"current_password":"x","otp":"123456","otp":"654321"}`},
			{"case alias", `{"Current_Password":"x"}`},
			{"factor case alias", `{"current_password":"x","OTP":"123456"}`},
			{"null password", `{"current_password":null}`},
			{"null factor", `{"current_password":"x","recovery_code":null}`},
			{"nonstring", `{"current_password":"x","otp":123456}`},
			{"unknown", `{"current_password":"x","user_id":"other"}`},
			{"missing password", `{"otp":"123456"}`},
			{"trailing", body + ` {}`},
			{"truncated", `{"current_password":`},
			{"array", `[]`},
			{"null body", `null`},
		} {
			t.Run(tc.name, func(t *testing.T) { mustStatus(t, post(t, "invalid", tc.body), http.StatusBadRequest) })
		}
		for _, tc := range []struct {
			name  string
			alter func(*http.Request)
		}{
			{"duplicate key", func(r *http.Request) { r.Header.Add("Idempotency-Key", "second") }},
			{"combined keys", func(r *http.Request) { r.Header.Set("Idempotency-Key", "first,second") }},
			{"missing key", func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
			{"duplicate cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "pulsemedia_session", Value: current}) }},
			{"duplicate content type", func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
			{"wrong content type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
			{"missing content type", func(r *http.Request) { r.Header.Del("Content-Type") }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				mustStatus(t, send(t, router, http.MethodPost, path, "invalid", body, current, "http://example.com", false, true, tc.alter), http.StatusBadRequest)
			})
		}
	})
	t.Run("reauthentication", func(t *testing.T) {
		for _, tc := range []struct{ name, body, code string }{
			{"wrong password", bodyFor("wrong-password", "recovery_code", oldCodes[1]), "REAUTHENTICATION_FAILED"},
			{"missing factor", `{"current_password":"` + password + `"}`, "REAUTHENTICATION_FAILED"},
			{"wrong factor", bodyFor(password, "recovery_code", "INVALID-RECOVERY"), "REAUTHENTICATION_FAILED"},
			{"both factors", body[:len(body)-1] + `,"otp":"123456"}`, "INVALID_ACCOUNT_COMMAND"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				w := post(t, "reauth-denied", tc.body)
				mustStatus(t, w, http.StatusBadRequest)
				if !strings.Contains(w.Body.String(), tc.code) {
					t.Fatal("wrong reauthentication error")
				}
			})
		}
		var receipts int
		if err := a.DBPool().QueryRow(ctx, "SELECT count(*) FROM account_security_command WHERE action=$1", action).Scan(&receipts); err != nil || receipts != 0 {
			t.Fatal("rejected request committed", err, receipts)
		}
	})
	var newCodes []string
	t.Run("OTP success safe original receipt and login", func(t *testing.T) {
		c.Advance(time.Minute)
		otpBody := bodyFor(password, "otp", otp())
		w := post(t, "regenerate-original", otpBody)
		mustStatus(t, w, http.StatusOK)
		var result ports.MFAConfirmResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Enabled || result.Replayed || len(result.RecoveryCodes) != 10 {
			t.Fatal("first response missing codes", err)
		}
		newCodes = result.RecoveryCodes
		seen := map[string]bool{}
		for _, code := range newCodes {
			if code == "" || seen[code] {
				t.Fatal("empty or duplicate recovery code")
			}
			seen[code] = true
		}
		assertSafe := func(w *httptest.ResponseRecorder) {
			t.Helper()
			for _, code := range append(append([]string{}, oldCodes...), newCodes...) {
				if strings.Contains(w.Body.String(), code) {
					t.Fatal("response exposed recovery code")
				}
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(w.Body.Bytes(), &fields) != nil {
				t.Fatal("non-JSON response")
			}
			if _, ok := fields["recovery_codes"]; ok {
				t.Fatal("safe response retained recovery_codes")
			}
		}
		// Recover the original command before any replay, as after a lost first response.
		w = send(t, router, http.MethodGet, "/api/v1/auth/security/commands/regenerate-original", "", "", current, "", false, true, nil)
		mustStatus(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), action) {
			t.Fatal("wrong original-key receipt")
		}
		assertSafe(w)
		w = post(t, "regenerate-original", otpBody)
		mustStatus(t, w, http.StatusOK)
		result = ports.MFAConfirmResult{}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Enabled || !result.Replayed || len(result.RecoveryCodes) != 0 {
			t.Fatal("unsafe original-key replay")
		}
		assertSafe(w)
		mustStatus(t, post(t, "regenerate-original", body), http.StatusConflict)
		w = send(t, router, http.MethodGet, "/api/v1/auth/mfa", "", "", current, "", false, true, nil)
		mustStatus(t, w, http.StatusOK)
		assertSafe(w)
		var status ports.MFAStatus
		if json.Unmarshal(w.Body.Bytes(), &status) != nil || !status.Enabled || status.RecoveryCodesRemaining != 10 {
			t.Fatal("MFA binding or replacement count changed")
		}
		var persisted string
		err := a.DBPool().QueryRow(ctx, `SELECT jsonb_build_object('receipts',(SELECT jsonb_agg(to_jsonb(r)) FROM account_security_command r),'audit',(SELECT jsonb_agg(to_jsonb(r)) FROM audit_log r),'events',(SELECT jsonb_agg(to_jsonb(r)) FROM outbox_event r),'mfa',(SELECT jsonb_agg(to_jsonb(r)) FROM account_totp r),'codes',(SELECT jsonb_agg(to_jsonb(r)) FROM account_totp_recovery r))::text`).Scan(&persisted)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range append(append([]string{password, setup.Secret}, oldCodes...), newCodes...) {
			if strings.Contains(persisted, secret) {
				t.Fatal("persisted plaintext MFA secret")
			}
		}
		for _, code := range oldCodes {
			var count int
			if err := a.DBPool().QueryRow(ctx, "SELECT count(*) FROM account_totp_recovery WHERE user_id=$1 AND code_hash=$2", user.ID, secrets.HashToken("pulse.mfa.recovery.v1:"+code)).Scan(&count); err != nil || count != 0 {
				t.Fatal("old code remains in replacement set", err)
			}
		}
		var receipts, audits, events int
		if err := a.DBPool().QueryRow(ctx, `SELECT (SELECT count(*) FROM account_security_command WHERE action=$1),(SELECT count(*) FROM audit_log WHERE action=$1),(SELECT count(*) FROM outbox_event WHERE event_type=$1)`, action).Scan(&receipts, &audits, &events); err != nil || receipts != 1 || audits != 1 || events != 1 {
			t.Fatal("replay duplicated business effects", err, receipts, audits, events)
		}
		for _, credential := range []string{other, token} {
			if _, err := login.AuthenticateToken(ctx, credential); err == nil {
				t.Fatal("other session or Token survived")
			}
		}
		if _, err := login.AuthenticateToken(ctx, current); err != nil {
			t.Fatal("current session revoked", err)
		}
		mustStatus(t, send(t, router, http.MethodGet, "/api/v1/auth/mfa", "", "", other, "", false, true, nil), http.StatusUnauthorized)
		loginBody := func(field, factor string) string {
			b, err := json.Marshal(map[string]string{"username": user.Username, "password": password, field: factor})
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
		w = send(t, router, http.MethodPost, "/api/v1/auth/login", "", loginBody("recovery_code", oldCodes[2]), "", "http://example.com", false, true, nil)
		mustStatus(t, w, http.StatusUnauthorized)
		assertSafe(w)
		w = send(t, router, http.MethodPost, "/api/v1/auth/login", "", loginBody("recovery_code", newCodes[0]), "", "http://example.com", false, true, nil)
		mustStatus(t, w, http.StatusOK)
		foundSession := false
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == "pulsemedia_session" && cookie.Value != "" {
				foundSession = true
			}
		}
		if !foundSession {
			t.Fatal("new recovery code did not create login session")
		}
		c.Advance(time.Minute)
		w = send(t, router, http.MethodPost, "/api/v1/auth/login", "", loginBody("otp", otp()), "", "http://example.com", false, true, nil)
		mustStatus(t, w, http.StatusOK)
	})
	if len(newCodes) != 10 {
		t.Fatal("success prerequisite failed")
	}
	t.Run("recovery factor success", func(t *testing.T) {
		input := bodyFor(password, "recovery_code", newCodes[1])
		w := post(t, "regenerate-with-recovery", input)
		mustStatus(t, w, http.StatusOK)
		var result ports.MFAConfirmResult
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Enabled || result.Replayed || len(result.RecoveryCodes) != 10 {
			t.Fatal("recovery factor did not regenerate")
		}
		w = post(t, "regenerate-with-recovery", input)
		mustStatus(t, w, http.StatusOK)
		var replay ports.MFAConfirmResult
		if json.Unmarshal(w.Body.Bytes(), &replay) != nil || !replay.Replayed || len(replay.RecoveryCodes) != 0 || strings.Contains(w.Body.String(), "recovery_codes") {
			t.Fatal("recovery factor replay exposed codes")
		}
	})
	t.Run("finite independent security rate budget", func(t *testing.T) {
		limited := httpapi.NewRouter(a, h, login, nil)
		for i := 0; i < 10; i++ {
			mustStatus(t, send(t, limited, http.MethodPost, path, "rate-invalid", `{}`, current, "http://example.com", false, false, nil), http.StatusBadRequest)
		}
		w := send(t, limited, http.MethodPost, path, "rate-invalid", `{}`, current, "http://example.com", false, false, nil)
		mustStatus(t, w, http.StatusTooManyRequests)
		if w.Header().Get("Retry-After") != "6" || !strings.Contains(w.Body.String(), "AUTH_RATE_LIMITED") {
			t.Fatal("missing bounded rate-limit evidence")
		}
		c.Advance(6 * time.Second)
		mustStatus(t, send(t, limited, http.MethodPost, path, "rate-invalid", `{}`, current, "http://example.com", false, false, nil), http.StatusBadRequest)
		mustStatus(t, send(t, limited, http.MethodPost, path, "rate-invalid", `{}`, current, "http://example.com", false, false, nil), http.StatusTooManyRequests)
	})
	// TOTP ciphertext must remain the original binding after either proof path.
	var bound []byte
	if err := a.DBPool().QueryRow(ctx, "SELECT secret_ciphertext FROM account_totp WHERE user_id=$1 AND enabled", user.ID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	plain, err := cipher.Decrypt(bound)
	expected, decodeErr := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	if err != nil || decodeErr != nil || !bytes.Equal(plain, expected) {
		t.Fatal("TOTP binding replaced", err, decodeErr)
	}
}
