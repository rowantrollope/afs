package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIKeyClientPaginationDoesNotLeakPartialResults(t *testing.T) {
	const id = "key_00000000000000000000000000000001"
	for _, failure := range []string{"", "unauthorized", "repeated-cursor"} {
		t.Run(failure, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "Bearer client-secret" || r.URL.Path != "/v1/api-keys" || r.URL.Query().Get("limit") != "100" {
					t.Error("invalid authenticated list request")
				}
				if requests == 1 {
					serverJSON(w, 200, APIKeyList{Keys: []APIKey{{ID: id, Name: "first"}}, Enabled: true, NextCursor: id})
					return
				}
				if r.URL.Query().Get("cursor") != id {
					t.Error("cursor not forwarded")
				}
				if failure == "unauthorized" {
					serverJSON(w, 401, map[string]string{"error": "client-secret"})
					return
				}
				next := ""
				if failure == "repeated-cursor" {
					next = id
				}
				serverJSON(w, 200, APIKeyList{Keys: []APIKey{{ID: "key_00000000000000000000000000000002", Name: "second"}}, Enabled: true, NextCursor: next})
			}))
			defer server.Close()
			client, err := NewCLIClient(server.URL, "client-secret")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			result, err := client.ListAPIKeys(context.Background())
			if requests != 2 {
				t.Fatalf("made %d requests", requests)
			}
			if failure == "" {
				if err != nil || !result.Enabled || len(result.Keys) != 2 || result.NextCursor != "" {
					t.Fatalf("incomplete list: %+v %v", result, err)
				}
			} else if err == nil || len(result.Keys) != 0 || strings.Contains(err.Error(), "client-secret") {
				t.Fatal("failure exposed partial results or credential")
			}
		})
	}
}

func TestAPIKeyClientNeverExpiryAndRevokeValidation(t *testing.T) {
	const id = "key_00000000000000000000000000000001"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.Method {
		case http.MethodPost:
			var input APIKeyCreateInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.Name != "automation" || input.ExpiresAt == nil || *input.ExpiresAt != "" {
				t.Error("never must be an explicit empty expires_at")
			}
			serverJSON(w, 200, APIKeyCreated{Key: APIKey{ID: id}, Token: "one-time-secret"})
		case http.MethodDelete:
			if r.URL.Path != "/v1/api-keys/"+id {
				t.Error("wrong revoke path")
			}
			serverJSON(w, 200, map[string]any{"key": APIKey{ID: id, Status: "revoked"}})
		default:
			t.Error("unexpected method")
		}
	}))
	defer server.Close()
	client, err := NewCLIClient(server.URL, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	created, err := client.CreateAPIKey(context.Background(), "automation", "")
	if err != nil || created.Token != "one-time-secret" {
		t.Fatal("create failed")
	}
	for _, bad := range []string{"", "../connection", "x?token=secret", "afs_key_secret", "key_0000000000000000000000000000000z"} {
		if _, err := client.RevokeAPIKey(context.Background(), bad); err == nil {
			t.Fatal("invalid identifier accepted")
		}
	}
	if requests != 1 {
		t.Fatal("invalid revoke made a network request")
	}
	key, err := client.RevokeAPIKey(context.Background(), id)
	if err != nil || key.Status != "revoked" {
		t.Fatal("revoke failed")
	}
}
