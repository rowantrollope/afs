package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8092", "192.168.1.1:8092", ":8092", "localhost:8092"} {
		if loopbackAddress(addr) == nil {
			t.Errorf("accepted %s", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8092", "[::1]:8092"} {
		if err := loopbackAddress(addr); err != nil {
			t.Error(err)
		}
	}
}

func TestHTTPTokenEveryRequestAndOrigin(t *testing.T) {
	token := strings.Repeat("private", 6)
	h := authorizedHandler(mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil), token)
	for _, method := range []string{"POST", "GET", "DELETE"} {
		for _, auth := range []string{"", "Bearer wrong"} {
			r := httptest.NewRequest(method, "http://127.0.0.1:8092/mcp", nil)
			r.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s token bypass: %d", method, w.Code)
			}
		}
	}
	for _, origin := range []string{"https://evil.example", "null", "http://127.0.0.1:8092/extra"} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8092/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("origin bypass %s: %d", origin, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://evil.example/mcp", nil)
	r.RemoteAddr = "127.0.0.1:1000"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("Host protection bypass: %d", w.Code)
	}
}
