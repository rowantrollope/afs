package controlplane

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func browserSessionTestHandler(t *testing.T) *serverHandler {
	t.Helper()
	h := NewHandler(nil, HandlerOptions{AuthToken: "test-team-token"}).(*serverHandler)
	h.authStore = metadataTestStore(t)
	return h
}

func browserSessionTestRequest(method, bearer, origin string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, "http://console.example/v1/auth/session", strings.NewReader("{}"))
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func browserSessionTestLogin(t *testing.T, h *serverHandler, bearer string, previous *http.Cookie) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	origin := "http://console.example"
	if h.options.SecureCookies {
		origin = "https://console.example"
	}
	h.browserSessionRoute(w, browserSessionTestRequest("POST", bearer, origin, previous))
	if w.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %d", len(cookies))
	}
	return cookies[0]
}

func browserSessionTestAuthenticate(t *testing.T, h *serverHandler, cookie *http.Cookie, bearer string, want bool) authenticatedIdentity {
	t.Helper()
	r := browserSessionTestRequest("GET", bearer, "", cookie)
	identity, valid, err := h.authenticate(r)
	if err != nil || valid != want {
		t.Fatalf("session authentication valid = %v, want %v, err = %v", valid, want, err)
	}
	return identity
}

func TestBrowserSessionCookieRotationAndLogout(t *testing.T) {
	h := browserSessionTestHandler(t)
	cookie := browserSessionTestLogin(t, h, h.options.AuthToken, nil)
	if cookie.Name != browserSessionCookie || cookie.Domain != "" || cookie.Path != "/" || !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge < int(browserSessionLifetime.Seconds())-1 || cookie.Expires.IsZero() {
		t.Fatalf("unsafe or nonpersistent browser cookie: %+v", cookie)
	}
	store := h.authStore.(*MetadataStore)
	var digest, parent, bootstrapHash string
	if err := store.db.QueryRow("SELECT hash, parent_key_id, bootstrap_hash FROM browser_sessions").Scan(&digest, &parent, &bootstrapHash); err != nil {
		t.Fatal(err)
	}
	if digest != apiKeyHash(cookie.Value) || parent != "" || bootstrapHash != apiKeyHash(h.options.AuthToken) {
		t.Fatal("session did not retain only credential hashes")
	}
	identity := browserSessionTestAuthenticate(t, h, cookie, "", true)
	if identity.Subject != "operator" {
		t.Fatalf("subject = %q", identity.Subject)
	}
	replacement := browserSessionTestLogin(t, h, h.options.AuthToken, cookie)
	if replacement.Value == cookie.Value {
		t.Fatal("session was not rotated")
	}
	browserSessionTestAuthenticate(t, h, cookie, "", false)
	browserSessionTestAuthenticate(t, h, replacement, "", true)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.browserSessionRoute(w, browserSessionTestRequest("DELETE", "", "http://console.example", replacement))
		if w.Code != http.StatusOK {
			t.Fatalf("logout %d: %d %s", i, w.Code, w.Body.String())
		}
		cleared := w.Result().Cookies()
		if len(cleared) != 1 || cleared[0].MaxAge != -1 || cleared[0].Value != "" {
			t.Fatalf("logout did not clear browser cookie: %+v", cleared)
		}
	}
	browserSessionTestAuthenticate(t, h, replacement, "", false)
}

func TestBrowserSessionParentRevocationExpiryAndBootstrapRotation(t *testing.T) {
	h := browserSessionTestHandler(t)
	store := h.authStore.(*MetadataStore)
	now := time.Now().UTC()
	parent, bearer := sqlTestCreateKey(t, store, "Laptop", now, false)
	// Shorten the key lifetime to prove the browser session cannot outlive it.
	expires := now.Add(time.Hour)
	if _, err := store.db.Exec("UPDATE api_keys SET expires_at = ?, expires_ms = ? WHERE id = ?", serverTime(expires), expires.UnixMilli(), parent.ID); err != nil {
		t.Fatal(err)
	}
	cookie := browserSessionTestLogin(t, h, bearer, nil)
	identity := browserSessionTestAuthenticate(t, h, cookie, "", true)
	if identity.KeyID != parent.ID || identity.Name != parent.Name || identity.Subject != "api-key:"+parent.ID {
		t.Fatalf("wrong session identity: %+v", identity)
	}
	var storedExpiry int64
	if err := store.db.QueryRow("SELECT expires_ms FROM browser_sessions WHERE hash = ?", apiKeyHash(cookie.Value)).Scan(&storedExpiry); err != nil || storedExpiry != expires.UnixMilli() {
		t.Fatalf("parent expiry was not inherited: %d %v", storedExpiry, err)
	}
	if _, valid, err := store.authenticateBrowserSession(context.Background(), cookie.Value, h.options.AuthToken, expires); err != nil || valid {
		t.Fatalf("session accepted at expiry: %v %v", valid, err)
	}
	if _, err := store.db.Exec("UPDATE api_keys SET expires_ms = ? WHERE id = ?", now.Add(-time.Second).UnixMilli(), parent.ID); err != nil {
		t.Fatal(err)
	}
	browserSessionTestAuthenticate(t, h, cookie, "", false)
	if _, err := store.db.Exec("UPDATE api_keys SET expires_ms = ? WHERE id = ?", expires.UnixMilli(), parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.revokeAPIKey(context.Background(), parent.ID, now); err != nil {
		t.Fatal(err)
	}
	browserSessionTestAuthenticate(t, h, cookie, "", false)
	bootstrap := browserSessionTestLogin(t, h, h.options.AuthToken, nil)
	h.options.AuthToken = "rotated-team-token"
	browserSessionTestAuthenticate(t, h, bootstrap, "", false)
}

func TestBrowserSessionExplicitBearerNeverFallsBack(t *testing.T) {
	h := browserSessionTestHandler(t)
	cookie := browserSessionTestLogin(t, h, h.options.AuthToken, nil)
	for _, value := range []string{"", "Basic ignored", "Bearer ", "Bearer invalid"} {
		r := browserSessionTestRequest("GET", "", "", cookie)
		r.Header.Set("Authorization", value)
		if _, valid, err := h.authenticate(r); err != nil || valid {
			t.Fatalf("explicit invalid Authorization %q fell back to cookie: %v %v", value, valid, err)
		}
	}
	browserSessionTestAuthenticate(t, h, cookie, h.options.AuthToken, true)
	w := httptest.NewRecorder()
	h.browserSessionRoute(w, browserSessionTestRequest("POST", "", "http://console.example", cookie))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cookie renewed itself: %d", w.Code)
	}
}

func TestBrowserSessionOriginProtection(t *testing.T) {
	h := browserSessionTestHandler(t)
	h.options.AllowedOrigins = []string{"http://localhost:5173"}
	cookie := browserSessionTestLogin(t, h, h.options.AuthToken, nil)
	for _, origin := range []string{"", "null", "http://attacker.example", "https://console.example", "http://console.example.attacker.example", "http://console.example/path", "http://console.example#ignored", "http://console.example?ignored"} {
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			r := browserSessionTestRequest(method, "", origin, cookie)
			if h.validateBrowserSessionOrigin(r) {
				t.Errorf("cookie %s accepted origin %q", method, origin)
			}
		}
		w := httptest.NewRecorder()
		h.browserSessionRoute(w, browserSessionTestRequest("POST", h.options.AuthToken, origin, nil))
		if w.Code != http.StatusForbidden {
			t.Errorf("login accepted origin %q: %d", origin, w.Code)
		}
	}
	for _, origin := range []string{"http://console.example", "http://localhost:5173"} {
		if !h.validateBrowserSessionOrigin(browserSessionTestRequest("POST", "", origin, cookie)) {
			t.Errorf("trusted origin %q rejected", origin)
		}
	}
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data"} {
		r := browserSessionTestRequest("POST", "", "http://console.example", cookie)
		r.Header.Set("Content-Type", contentType)
		if h.validateBrowserSessionOrigin(r) {
			t.Errorf("cookie POST accepted simple content type %q", contentType)
		}
	}
	if !h.validateBrowserSessionOrigin(browserSessionTestRequest("POST", h.options.AuthToken, "", cookie)) || !h.validateBrowserSessionOrigin(browserSessionTestRequest("POST", "", "", nil)) {
		t.Fatal("origin guard blocked non-cookie CLI requests")
	}
	if !h.validateBrowserSessionOrigin(browserSessionTestRequest("GET", "", "", cookie)) {
		t.Fatal("read request required an Origin")
	}
	w := httptest.NewRecorder()
	h.browserSessionRoute(w, browserSessionTestRequest("DELETE", "", "", cookie))
	if w.Code != http.StatusForbidden {
		t.Fatal("cross-origin logout accepted")
	}
	browserSessionTestAuthenticate(t, h, cookie, "", true)
}

func TestBrowserSessionSecureCookiesAndUntrustedForwardedHeaders(t *testing.T) {
	h := browserSessionTestHandler(t)
	h.options.SecureCookies = true
	cookie := browserSessionTestLogin(t, h, h.options.AuthToken, nil)
	if !cookie.Secure {
		t.Fatal("hosted HTTPS session cookie was not Secure")
	}
	h.options.SecureCookies = false
	r := browserSessionTestRequest("POST", h.options.AuthToken, "https://console.example", nil)
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	h.browserSessionRoute(w, r)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].Secure {
		t.Fatal("native HTTPS session cookie was not Secure")
	}
	r = browserSessionTestRequest("POST", h.options.AuthToken, "http://console.example", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	w = httptest.NewRecorder()
	h.browserSessionRoute(w, r)
	if w.Code != http.StatusOK || w.Result().Cookies()[0].Secure {
		t.Fatal("untrusted forwarded header changed cookie security")
	}
}

func TestBrowserSessionExpiryCleanupAndStorageFailure(t *testing.T) {
	h := browserSessionTestHandler(t)
	store := h.authStore.(*MetadataStore)
	cookie := browserSessionTestLogin(t, h, h.options.AuthToken, nil)
	if _, err := store.db.Exec("UPDATE browser_sessions SET expires_ms = 0"); err != nil {
		t.Fatal(err)
	}
	browserSessionTestAuthenticate(t, h, cookie, "", false)
	replacement := browserSessionTestLogin(t, h, h.options.AuthToken, nil)
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM browser_sessions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("expired session not removed: count = %d, err = %v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, valid, err := h.authenticate(browserSessionTestRequest("GET", "", "", replacement)); err == nil || valid {
		t.Fatalf("storage failure did not fail closed: %v %v", valid, err)
	}
	for _, method := range []string{"POST", "DELETE"} {
		w := httptest.NewRecorder()
		h.browserSessionRoute(w, browserSessionTestRequest(method, h.options.AuthToken, "http://console.example", replacement))
		if w.Code != http.StatusServiceUnavailable || len(w.Result().Cookies()) != 0 {
			t.Fatalf("storage failure %s: %d", method, w.Code)
		}
	}
}

func TestBrowserSessionDisabledWithoutMetadataOrAuthentication(t *testing.T) {
	h := NewHandler(nil, HandlerOptions{AuthToken: "test-token"}).(*serverHandler)
	if h.browserSessionAvailable() {
		t.Fatal("session feature available without metadata")
	}
	h = browserSessionTestHandler(t)
	h.options.AuthToken = ""
	if h.browserSessionAvailable() {
		t.Fatal("session feature available without authentication")
	}
}

func TestBrowserSessionHTTPRoutesWorkWithoutRedisAndKeepScopedURLs(t *testing.T) {
	h := emptyMetadataHandler(t, filepath.Join(t.TempDir(), "metadata.sqlite"), "")
	login := httptest.NewRecorder()
	h.ServeHTTP(login, browserSessionTestRequest("POST", "test-secret", "http://console.example", nil))
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("offline login: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	for _, path := range []string{"/v1/auth/config", "/v1/auth/verify", "/databases/offline/v1/auth/config", "/databases/offline/v1/auth/verify"} {
		r := httptest.NewRequest("GET", "http://console.example"+path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"authenticated":true`) {
			t.Fatalf("offline scoped auth %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, origin := range []string{"", "http://console.example"} {
		r := browserSessionTestRequest("POST", "", origin, cookie)
		r.URL.Path = "/v1/api-keys"
		r.Body = http.NoBody
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if origin == "" && w.Code != http.StatusForbidden {
			t.Fatalf("unsafe cookie mutation reached API: %d", w.Code)
		}
		if origin != "" && w.Code == http.StatusForbidden {
			t.Fatalf("same-origin cookie mutation rejected: %d", w.Code)
		}
	}
	r := browserSessionTestRequest("DELETE", "", "http://console.example", cookie)
	r.URL.Path = "/databases/offline/v1/auth/session"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("scoped logout: %d %s", w.Code, w.Body.String())
	}
}
