package controlplane

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestDatabaseRegistrationAuthValidationAndAtomicSave(t *testing.T) {
	service, _ := serviceFixture(t)
	filename := filepath.Join(t.TempDir(), "databases.json")
	h, err := NewDatabaseHandler(service, HandlerOptions{AuthToken: "test-secret"}, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	candidate := miniredis.RunT(t)
	input := map[string]any{"name": "Extra", "redis_addr": candidate.Addr(), "redis_db": 0}
	if r := historyHTTPCall(t, h, "POST", "/v1/databases", input, nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated save: %d", r.Code)
	}
	if r := historyHTTPCall(t, h, "POST", "/v1/databases", input, map[string]string{"Authorization": "Bearer test-secret", "Origin": "https://evil.invalid"}); r.Code != http.StatusForbidden {
		t.Fatalf("cross-origin save: %d", r.Code)
	}
	for _, invalid := range []map[string]any{
		{"name": "", "redis_addr": candidate.Addr()},
		{"name": "Secret", "redis_addr": "redis://user:private-password@example.com:6379"},
		{"name": "Extra", "redis_addr": candidate.Addr(), "redis_db": -1},
		{"name": "Extra", "redis_addr": candidate.Addr(), "redis_db": "private-password"},
		{"name": "Extra", "redis_addr": candidate.Addr(), "private-password": true},
	} {
		r := serverTestCall(t, h, "POST", "/v1/databases", invalid)
		if r.Code != 400 || strings.Contains(r.Body.String(), "private-password") {
			t.Fatalf("validation: %d %s", r.Code, r.Body.String())
		}
	}
	// An unwritable destination cannot publish a connection visible only until restart.
	if err := os.Mkdir(filename, 0700); err != nil {
		t.Fatal(err)
	}
	r := serverTestCall(t, h, "POST", "/v1/databases", input)
	if r.Code != 400 || len(h.root.databaseHandlers()) != 1 {
		t.Fatalf("failed save published profile: %d %s", r.Code, r.Body.String())
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	r = serverTestCall(t, h, "POST", "/v1/databases", input)
	if r.Code != 201 {
		t.Fatalf("save: %d %s", r.Code, r.Body.String())
	}
	var added map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added["is_default"] != false {
		t.Fatalf("added database became default: %v", added)
	}
	for _, duplicate := range []map[string]any{
		{"name": "Extra", "redis_addr": candidate.Addr(), "redis_db": 1},
		{"name": "Different", "redis_addr": candidate.Addr(), "redis_db": 0},
	} {
		if r := serverTestCall(t, h, "POST", "/v1/databases", duplicate); r.Code != 409 {
			t.Fatalf("duplicate: %d %s", r.Code, r.Body.String())
		}
	}
	if _, err := NewDatabaseHandler(service, HandlerOptions{}, filename); err == nil {
		t.Fatal("two servers acquired the same configuration")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewDatabaseHandler(service, HandlerOptions{}, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.lookup(added["id"].(string)) == nil {
		t.Fatal("saved profile missing after restart")
	}
}

func TestConcurrentDatabaseRegistrationDoesNotLoseConnections(t *testing.T) {
	service, _ := serviceFixture(t)
	filename := filepath.Join(t.TempDir(), "databases.json")
	h, err := NewDatabaseHandler(service, HandlerOptions{AuthToken: "test-secret"}, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	first, second := miniredis.RunT(t), miniredis.RunT(t)
	var wg sync.WaitGroup
	for _, input := range []map[string]any{{"name": "First", "redis_addr": first.Addr()}, {"name": "Second", "redis_addr": second.Addr()}} {
		wg.Add(1)
		go func(input map[string]any) {
			defer wg.Done()
			r := serverTestCall(t, h, "POST", "/v1/databases", input)
			if r.Code != 201 {
				t.Errorf("save: %d %s", r.Code, r.Body.String())
			}
		}(input)
	}
	wg.Wait()
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var profiles []databaseProfile
	if err := json.Unmarshal(raw, &profiles); err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || len(h.root.databaseHandlers()) != 3 {
		t.Fatalf("lost concurrent registration: %s", raw)
	}
}
