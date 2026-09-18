package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnauthenticatedLoopbackHostGuard(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := guardUnauthenticatedLoopbackHost(next, "")
	for _, host := range []string{"localhost", "LOCALHOST:8091", "127.0.0.1:8091", "127.1.2.3", "[::1]:8091", "[::1]", "[::ffff:127.0.0.1]:8091"} {
		t.Run("allow_"+host, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://localhost/v1/workspaces", nil)
			request.Host = host
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("Host %q: HTTP %d", host, response.Code)
			}
		})
	}
	for _, host := range []string{"rebind.example:8091", "localhost.evil.example", "127.0.0.1.evil.example", "192.168.1.2:8091", "[2001:db8::1]:8091", "", "[::1]evil"} {
		for _, path := range []string{"/", "/assets/app.js", "/v1/workspaces", "/healthz"} {
			request := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
			request.Host = host
			// DNS rebinding makes an attacker page appear same-origin to the API.
			request.Header.Set("Origin", "http://"+host)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("Host %q path %s: HTTP %d", host, path, response.Code)
			}
		}
	}
}

func TestTokenDeploymentPreservesProxyHost(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "https://afs.example/v1/workspaces", nil)
	response := httptest.NewRecorder()
	guardUnauthenticatedLoopbackHost(next, "configured-token").ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("token deployment Host blocked: HTTP %d", response.Code)
	}
}
