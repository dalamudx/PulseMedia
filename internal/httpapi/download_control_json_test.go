package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrictDownloadControlJSON(t *testing.T) {
	const path = "/api/v1/operations/12345678-1234-4234-8234-123456789abc/download-control"
	for _, body := range []string{`{"action":"pause","confirmed":true}`, `{"action":"resume","confirmed":true,"affect_shared":false}`} {
		called := false
		h := StrictDownloadControlJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }))
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "original")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if !called || w.Code != 204 {
			t.Fatal("valid rejected", w.Code)
		}
	}
	for _, body := range []string{`null`, `[]`, `{}`, `{"action":"pause","confirmed":null}`, `{"action":"pause","confirmed":false}`, `{"action":"pause","confirmed":true,"affect_shared":null}`, `{"action":"pause","confirmed":true,"confirmed":true}`, `{"action":"pause","confirmed":true,"confi\u0072med":true}`, `{"Action":"pause","confirmed":true}`, `{"action":"pause","confirmed":true,"owner_id":"other"}`, `{"action":"pause","confirmed":true,"affect_shared":{}}`, `{"action":"pause","confirmed":true} {}`} {
		h := StrictDownloadControlJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid reached handler", body) }))
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "original")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 400 || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal(body, w.Code)
		}
	}
	for _, body := range []string{"", `{}`, `null`} {
		called := false
		h := StrictDownloadControlJSON(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) }))
		req := httptest.NewRequest(http.MethodPost, path+"/observations", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "original")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if called != (body == "") {
			t.Fatal("observation body admitted", body, w.Code)
		}
	}
	h := StrictDownloadControlJSON(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("repeated key admitted") }))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Add("Idempotency-Key", "one")
	req.Header.Add("Idempotency-Key", "two")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
