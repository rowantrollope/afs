package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func cliLoginTestHTTP(t *testing.T, h http.Handler, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, r)
	return response
}

func cliLoginTestChallenge() (string, string) {
	verifier := strings.Repeat("a", 43)
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:])
}

func cliLoginTestStart(t *testing.T, s *MetadataStore, now time.Time) CLILoginStart {
	t.Helper()
	_, challenge := cliLoginTestChallenge()
	request, err := s.startCLILogin(context.Background(), "AFS CLI on test laptop", challenge, now)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func cliLoginTestApprove(t *testing.T, s *MetadataStore, request CLILoginStart, now time.Time) {
	t.Helper()
	identity := authenticatedIdentity{Subject: "operator", store: s}
	status, err := s.decideCLILogin(context.Background(), request.ID, request.UserCode, "approve", identity, apiKeyHash("test-secret"), now)
	if err != nil || status != "approved" {
		t.Fatalf("approve: status=%q error=%v", status, err)
	}
}

func TestCLILoginHTTPApprovalAndAuthentication(t *testing.T) {
	h := emptyMetadataHandler(t, filepath.Join(t.TempDir(), "metadata.sqlite"), "")
	server := httptest.NewServer(h)
	defer server.Close()
	client, err := NewCLIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	config, err := client.GetAuthConfig(ctx)
	if err != nil || !config.Enabled || !config.BrowserLogin || config.Authenticated {
		t.Fatalf("config: %+v %v", config, err)
	}
	verifier, challenge := cliLoginTestChallenge()
	request, err := client.StartCLILogin(ctx, "CLI on my laptop", challenge)
	if err != nil {
		t.Fatal(err)
	}
	if !validCLILoginID(request.ID) || !validCLILoginSecret(request.DeviceCode) || request.VerificationPath != "/connect-cli?request="+request.ID || strings.Contains(request.VerificationPath, request.DeviceCode) || strings.Contains(request.VerificationPath, request.UserCode) {
		t.Fatalf("invalid start response: %+v", request)
	}
	path := "/v1/auth/cli/requests/" + request.ID
	for _, method := range []string{"GET", "POST"} {
		response := cliLoginTestHTTP(t, h, "", method, path, map[string]string{"decision": "approve", "user_code": request.UserCode})
		if response.Code != 401 {
			t.Fatalf("unauthenticated approval metadata: %d %s", response.Code, response.Body.String())
		}
	}
	view := cliLoginTestHTTP(t, h, "test-secret", "GET", path, nil)
	if view.Code != 200 || !strings.Contains(view.Body.String(), request.UserCode) || strings.Contains(view.Body.String(), request.DeviceCode) || strings.Contains(view.Body.String(), challenge) {
		t.Fatalf("approval view: %d %s", view.Code, view.Body.String())
	}
	var storedStatus string
	if err := h.metadata.db.QueryRow("SELECT status FROM cli_login_requests WHERE id = ?", request.ID).Scan(&storedStatus); err != nil || storedStatus != "pending" {
		t.Fatalf("reading approval page granted access: status=%q err=%v", storedStatus, err)
	}
	wrong := cliLoginTestHTTP(t, h, "test-secret", "POST", path, map[string]string{"decision": "approve", "user_code": "WRNG-CODE"})
	if wrong.Code != 400 {
		t.Fatalf("incorrect confirmation code: %d", wrong.Code)
	}
	approved := cliLoginTestHTTP(t, h, "test-secret", "POST", path, map[string]string{"decision": "approve", "user_code": request.UserCode})
	if approved.Code != 200 {
		t.Fatalf("approve: %d %s", approved.Code, approved.Body.String())
	}
	polled, err := client.PollCLILogin(ctx, request.DeviceCode, verifier)
	if err != nil || polled.Status != "complete" || polled.Key == nil || polled.Token == "" {
		t.Fatalf("exchange: %+v %v", polled, err)
	}
	authenticated, err := NewCLIClient(server.URL, polled.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer authenticated.Close()
	if err := authenticated.VerifyAuthentication(ctx); err != nil {
		t.Fatalf("minted key unusable: %v", err)
	}
	if response := cliLoginTestHTTP(t, h, "test-secret", "POST", path, map[string]string{"decision": "approve", "user_code": request.UserCode}); response.Code != 409 {
		t.Fatalf("reapprove consumed grant: %d", response.Code)
	}
	polled, err = client.PollCLILogin(ctx, request.DeviceCode, verifier)
	if err != nil || polled.Status != "denied" || polled.Token != "" {
		t.Fatalf("replayed exchange: %+v %v", polled, err)
	}
}

func TestCLILoginPKCEPendingThrottleAndHashedStorage(t *testing.T) {
	s := metadataTestStore(t)
	now := time.Now().Truncate(time.Millisecond)
	request := cliLoginTestStart(t, s, now)
	verifier, _ := cliLoginTestChallenge()
	ctx := context.Background()
	var deviceHash, challenge string
	if err := s.db.QueryRow("SELECT device_hash, challenge FROM cli_login_requests WHERE id = ?", request.ID).Scan(&deviceHash, &challenge); err != nil {
		t.Fatal(err)
	}
	if deviceHash != apiKeyHash(request.DeviceCode) || strings.Contains(deviceHash, request.DeviceCode) || challenge == verifier {
		t.Fatal("plaintext CLI proof stored")
	}
	bad, err := s.exchangeCLILogin(ctx, request.DeviceCode, strings.Repeat("b", 43), apiKeyHash("test-secret"), now)
	if err != nil || bad.Status != "denied" {
		t.Fatalf("wrong verifier: %+v %v", bad, err)
	}
	pending, err := s.exchangeCLILogin(ctx, request.DeviceCode, verifier, apiKeyHash("test-secret"), now)
	if err != nil || pending.Status != "pending" || pending.Interval != 2 {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	cliLoginTestApprove(t, s, request, now)
	early, err := s.exchangeCLILogin(ctx, request.DeviceCode, verifier, apiKeyHash("test-secret"), now.Add(time.Second))
	if err != nil || early.Status != "pending" || early.Token != "" {
		t.Fatalf("poll throttle: %+v %v", early, err)
	}
	result, err := s.exchangeCLILogin(ctx, request.DeviceCode, verifier, apiKeyHash("test-secret"), now.Add(2*time.Second))
	if err != nil || result.Status != "complete" {
		t.Fatalf("legitimate proof consumed by failed proof: %+v %v", result, err)
	}
	var storedHash string
	if err := s.db.QueryRow("SELECT hash FROM api_keys WHERE id = ?", result.Key.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != apiKeyHash(result.Token) {
		t.Fatal("API key digest mismatch")
	}
	if result.Key.Name != "AFS CLI on test laptop" {
		t.Fatalf("missing client name: %s", result.Key.Name)
	}
	expires, _ := time.Parse(time.RFC3339Nano, result.Key.ExpiresAt)
	if expires.Sub(now) > 30*24*time.Hour {
		t.Fatal("CLI key lives longer than 30 days")
	}
}

func TestCLILoginReplayRace(t *testing.T) {
	s := metadataTestStore(t)
	cliLoginReplayRace(t, s, s)
}

func cliLoginReplayRace(t *testing.T, startStore, exchangeStore *MetadataStore) {
	t.Helper()
	now := time.Now()
	request := cliLoginTestStart(t, startStore, now)
	cliLoginTestApprove(t, startStore, request, now)
	verifier, _ := cliLoginTestChallenge()
	var wg sync.WaitGroup
	results := make(chan CLILoginPoll, 24)
	errors := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := startStore
			if i%2 == 0 {
				store = exchangeStore
			}
			result, err := store.exchangeCLILogin(context.Background(), request.DeviceCode, verifier, apiKeyHash("test-secret"), now)
			results <- result
			errors <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	completed := 0
	for result := range results {
		if result.Status == "complete" {
			completed++
		} else if result.Status != "denied" {
			t.Fatalf("unexpected race result: %+v", result)
		}
	}
	var count int
	if err := startStore.db.QueryRow(startStore.querySQL("SELECT COUNT(*) FROM api_keys")).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if completed != 1 || count != 1 {
		t.Fatalf("grant replay issued %d responses and %d keys", completed, count)
	}
}

func TestCLILoginDeniedExpiredAndBootstrapRotation(t *testing.T) {
	for _, scenario := range []string{"denied", "expired", "rotated"} {
		t.Run(scenario, func(t *testing.T) {
			s := metadataTestStore(t)
			ctx := context.Background()
			now := time.Now()
			request := cliLoginTestStart(t, s, now)
			verifier, _ := cliLoginTestChallenge()
			hash := apiKeyHash("test-secret")
			want := "denied"
			if scenario == "denied" {
				status, err := s.decideCLILogin(ctx, request.ID, request.UserCode, "deny", authenticatedIdentity{Subject: "operator", store: s}, hash, now)
				if err != nil || status != "denied" {
					t.Fatalf("deny: %s %v", status, err)
				}
			} else {
				cliLoginTestApprove(t, s, request, now)
			}
			if scenario == "expired" {
				now = now.Add(cliLoginLifetime)
				want = "expired"
			}
			if scenario == "rotated" {
				hash = apiKeyHash("replacement-team-token")
			}
			result, err := s.exchangeCLILogin(ctx, request.DeviceCode, verifier, hash, now)
			if err != nil || result.Status != want || result.Token != "" {
				t.Fatalf("exchange: %+v %v", result, err)
			}
			var count int
			if err := s.db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&count); err != nil || count != 0 {
				t.Fatalf("denied request minted a key: %d %v", count, err)
			}
		})
	}
}

func TestCLILoginParentRevocationAndExpiryCap(t *testing.T) {
	for _, scenario := range []string{"cap", "revoked", "expired", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			s := metadataTestStore(t)
			ctx := context.Background()
			now := time.Now().Truncate(time.Millisecond)
			parentExpiry := now.Add(time.Minute)
			parent, _, err := s.createAPIKey(ctx, APIKey{Name: "Parent admin", CreatedAt: serverTime(now), ExpiresAt: serverTime(parentExpiry)})
			if err != nil {
				t.Fatal(err)
			}
			request := cliLoginTestStart(t, s, now)
			identity := authenticatedIdentity{Subject: "api-key:" + parent.ID, KeyID: parent.ID, store: s}
			status, err := s.decideCLILogin(ctx, request.ID, request.UserCode, "approve", identity, apiKeyHash("test-secret"), now)
			if err != nil || status != "approved" {
				t.Fatalf("approve: %s %v", status, err)
			}
			switch scenario {
			case "revoked":
				if _, err := s.revokeAPIKey(ctx, parent.ID, now); err != nil {
					t.Fatal(err)
				}
			case "expired":
				now = parentExpiry
			case "deleted":
				if _, err := s.db.Exec("DELETE FROM api_keys WHERE id = ?", parent.ID); err != nil {
					t.Fatal(err)
				}
			}
			verifier, _ := cliLoginTestChallenge()
			result, err := s.exchangeCLILogin(ctx, request.DeviceCode, verifier, apiKeyHash("test-secret"), now)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "cap" {
				if result.Status != "complete" || result.Key.ExpiresAt != parent.ExpiresAt {
					t.Fatalf("parent expiry not inherited: %+v", result)
				}
			} else if result.Status != "denied" || result.Token != "" {
				t.Fatalf("stale approval minted token: %+v", result)
			}
		})
	}
}

func TestCLILoginMintFailureRollsBackGrant(t *testing.T) {
	s := metadataTestStore(t)
	now := time.Now()
	ctx := context.Background()
	request := cliLoginTestStart(t, s, now)
	cliLoginTestApprove(t, s, request, now)
	if _, err := s.db.Exec("CREATE TRIGGER fail_cli_key BEFORE INSERT ON api_keys BEGIN SELECT RAISE(ABORT, 'simulated key failure'); END"); err != nil {
		t.Fatal(err)
	}
	verifier, _ := cliLoginTestChallenge()
	if _, err := s.exchangeCLILogin(ctx, request.DeviceCode, verifier, apiKeyHash("test-secret"), now); err == nil {
		t.Fatal("expected mint failure")
	}
	var status string
	if err := s.db.QueryRow("SELECT status FROM cli_login_requests WHERE id = ?", request.ID).Scan(&status); err != nil || status != "approved" {
		t.Fatalf("mint failure consumed grant: %s %v", status, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_cli_key"); err != nil {
		t.Fatal(err)
	}
	result, err := s.exchangeCLILogin(ctx, request.DeviceCode, verifier, apiKeyHash("test-secret"), now)
	if err != nil || result.Status != "complete" {
		t.Fatalf("retry after rolled-back mint: %+v %v", result, err)
	}
}

func TestCLILoginCapacityCleanupAndPersistence(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "metadata.sqlite")
	s, err := OpenMetadataStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	ctx := context.Background()
	now := time.Now()
	request := cliLoginTestStart(t, s, now)
	cliLoginTestApprove(t, s, request, now)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenMetadataStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge := cliLoginTestChallenge()
	result, err := s.exchangeCLILogin(ctx, request.DeviceCode, verifier, apiKeyHash("test-secret"), now)
	if err != nil || result.Status != "complete" {
		t.Fatalf("restart lost approval: %+v %v", result, err)
	}
	for i := 1; i < cliLoginMaxOutstanding; i++ {
		cliLoginTestStart(t, s, now)
	}
	if _, err := s.startCLILogin(ctx, "over capacity", challenge, now); !errors.Is(err, errCLILoginCapacity) {
		t.Fatalf("unbounded login requests: %v", err)
	}
	if _, err := s.startCLILogin(ctx, "after expiry", challenge, now.Add(cliLoginLifetime)); err != nil {
		t.Fatalf("expired grants prevent sign-in: %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM cli_login_requests").Scan(&count); err != nil || count != 1 {
		t.Fatalf("expired cleanup: %d %v", count, err)
	}
}

func TestCLILoginHTTPValidationAvailabilityAndSecrets(t *testing.T) {
	h := emptyMetadataHandler(t, filepath.Join(t.TempDir(), "metadata.sqlite"), "")
	_, challenge := cliLoginTestChallenge()
	for _, input := range []map[string]string{
		{"name": "", "code_challenge": challenge},
		{"name": strings.Repeat("x", 129), "code_challenge": challenge},
		{"name": "client\nforged", "code_challenge": challenge},
		{"name": "client", "code_challenge": "not-s256"},
		{"name": "client", "code_challenge": challenge, "unknown": "field"},
	} {
		response := cliLoginTestHTTP(t, h, "", "POST", "/v1/auth/cli/start", input)
		if response.Code != 400 {
			t.Fatalf("invalid request accepted: %d %s", response.Code, response.Body.String())
		}
	}
	for _, path := range []string{"/v1/auth/cli/start", "/v1/auth/cli/token"} {
		if response := cliLoginTestHTTP(t, h, "", "GET", path, nil); response.Code != 405 {
			t.Fatalf("invalid method accepted: %d", response.Code)
		}
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"name":"client"}`))
		r.Header.Set("Content-Type", "text/plain")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		if response.Code != 415 {
			t.Fatalf("simple cross-site form accepted: %d", response.Code)
		}
	}
	request := cliLoginTestStart(t, h.metadata, time.Now())
	response := cliLoginTestHTTP(t, h, "test-secret", "GET", "/v1/auth/cli/requests/"+request.ID, nil)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("approval may be cached")
	}
	if err := h.metadata.Close(); err != nil {
		t.Fatal(err)
	}
	response = cliLoginTestHTTP(t, h, "", "POST", "/v1/auth/cli/start", map[string]string{"name": "client", "code_challenge": challenge})
	if response.Code != 503 || strings.Contains(response.Body.String(), "sqlite") {
		t.Fatalf("store failure: %d %s", response.Code, response.Body.String())
	}
	// Malicious servers must not reflect polling proof into CLI error output.
	verifier, _ := cliLoginTestChallenge()
	malicious := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("bad %s %s", request.DeviceCode, verifier)})
	}))
	defer malicious.Close()
	client, _ := NewCLIClient(malicious.URL, "")
	defer client.Close()
	_, err := client.PollCLILogin(context.Background(), request.DeviceCode, verifier)
	if err == nil || strings.Contains(err.Error(), request.DeviceCode) || strings.Contains(err.Error(), verifier) {
		t.Fatalf("poll error exposed proof: %v", err)
	}
}

func TestPostgresCLILoginExchangeAcrossInstances(t *testing.T) {
	dsn := postgresTestDatabase(t)
	first, second := postgresTestStore(t, dsn), postgresTestStore(t, dsn)
	cliLoginReplayRace(t, first, second)
}

func TestPostgresCLILoginBrowserSessionsAcrossInstances(t *testing.T) {
	dsn := postgresTestDatabase(t)
	first, second := postgresTestStore(t, dsn), postgresTestStore(t, dsn)
	ctx := context.Background()
	now := time.Now()
	bootstrap, _, err := first.createBrowserSession(ctx, authenticatedIdentity{Subject: "operator", store: first}, "test-secret", "", now)
	if err != nil {
		t.Fatal(err)
	}
	identity, valid, err := second.authenticateBrowserSession(ctx, bootstrap, "test-secret", now)
	if err != nil || !valid || identity.store != second || identity.Subject != "operator" {
		t.Fatalf("shared bootstrap session: %+v %v %v", identity, valid, err)
	}
	if _, valid, err := second.authenticateBrowserSession(ctx, bootstrap, "rotated-secret", now); err != nil || valid {
		t.Fatalf("bootstrap rotation ignored: %v %v", valid, err)
	}
	rotated, _, err := second.createBrowserSession(ctx, identity, "test-secret", bootstrap, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, valid, err := first.authenticateBrowserSession(ctx, bootstrap, "test-secret", now); err != nil || valid {
		t.Fatalf("session rotation not shared: %v %v", valid, err)
	}
	if err := first.revokeBrowserSession(ctx, rotated); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := second.authenticateBrowserSession(ctx, rotated, "test-secret", now); err != nil || valid {
		t.Fatalf("session revocation not shared: %v %v", valid, err)
	}
	parent, _, err := first.createAPIKey(ctx, APIKey{Name: "Parent admin", CreatedAt: serverTime(now), ExpiresAt: serverTime(now.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	parentIdentity := authenticatedIdentity{Subject: "api-key:" + parent.ID, KeyID: parent.ID, store: first}
	session, _, err := first.createBrowserSession(ctx, parentIdentity, "test-secret", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, valid, err := second.authenticateBrowserSession(ctx, session, "test-secret", now); err != nil || !valid {
		t.Fatalf("shared parent session invalid: %v %v", valid, err)
	}
	if _, err := second.revokeAPIKey(ctx, parent.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := first.authenticateBrowserSession(ctx, session, "test-secret", now); err != nil || valid {
		t.Fatalf("parent revocation not shared: %v %v", valid, err)
	}
}
