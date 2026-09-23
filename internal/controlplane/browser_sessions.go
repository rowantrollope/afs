package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const browserSessionCookie = "afs_browser_session"
const browserSessionLifetime = 30 * 24 * time.Hour

// Only hashes are retained. Sessions stay in the metadata catalog so browser
// login works across server instances and independently of managed Redis.
const browserSessionSchema = `CREATE TABLE IF NOT EXISTS browser_sessions (
 hash TEXT PRIMARY KEY NOT NULL,
 parent_key_id TEXT NOT NULL DEFAULT '',
 bootstrap_hash TEXT NOT NULL DEFAULT '',
 created_ms BIGINT NOT NULL,
 expires_ms BIGINT NOT NULL
)`

var errBrowserSessionInvalid = errors.New("browser session is invalid or expired")

func (h *serverHandler) browserSessionAvailable() bool {
	store, ok := h.authStore.(*MetadataStore)
	return ok && store != nil && store.db != nil && h.options.AuthToken != ""
}

func (h *serverHandler) secureBrowserCookies(r *http.Request) bool {
	return r.TLS != nil || h.options.SecureCookies
}

// Cookie-authenticated mutations require an explicit trusted Origin. SameSite
// alone does not stop requests from an untrusted sibling subdomain. Explicit
// development origins remain usable with the Vite proxy.
func (h *serverHandler) validateBrowserSessionOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if _, present := r.Header["Authorization"]; present {
		return true
	}
	if _, err := r.Cookie(browserSessionCookie); err != nil {
		return true
	}
	return h.allowedBrowserSessionOrigin(r)
}

func (h *serverHandler) allowedBrowserSessionOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	scheme := "http"
	if h.secureBrowserCookies(r) {
		scheme = "https"
	}
	allowed := origin == scheme+"://"+r.Host
	for _, candidate := range h.options.AllowedOrigins {
		if origin == candidate {
			allowed = true
		}
	}
	if !allowed {
		return false
	}
	if r.Method != http.MethodDelete {
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			return false
		}
	}
	return true
}

func validBrowserSessionToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == token
}

func browserCookieToken(r *http.Request) string {
	cookie, err := r.Cookie(browserSessionCookie)
	if err != nil || !validBrowserSessionToken(cookie.Value) {
		return ""
	}
	return cookie.Value
}

func (s *MetadataStore) createBrowserSession(ctx context.Context, identity authenticatedIdentity, bootstrapToken, previous string, now time.Time) (string, time.Time, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", time.Time{}, errors.New("cannot generate browser session")
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	expires := now.Add(browserSessionLifetime)
	bootstrapHash := ""
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, errors.New("cannot create browser session")
	}
	defer tx.Rollback()
	if identity.KeyID != "" {
		var parentExpiry int64
		err := tx.QueryRowContext(ctx, s.querySQL(`SELECT expires_ms FROM api_keys
 WHERE id = ? AND revoked_at = '' AND (expires_ms = 0 OR expires_ms > ?)`), identity.KeyID, now.UnixMilli()).Scan(&parentExpiry)
		if errors.Is(err, sql.ErrNoRows) {
			return "", time.Time{}, errBrowserSessionInvalid
		}
		if err != nil {
			return "", time.Time{}, errors.New("cannot verify browser session parent")
		}
		if parentExpiry != 0 && parentExpiry < expires.UnixMilli() {
			expires = time.UnixMilli(parentExpiry)
		}
	} else {
		if bootstrapToken == "" {
			return "", time.Time{}, errBrowserSessionInvalid
		}
		bootstrapHash = apiKeyHash(bootstrapToken)
	}
	if _, err := tx.ExecContext(ctx, s.querySQL(`DELETE FROM browser_sessions WHERE hash IN
 (SELECT hash FROM browser_sessions WHERE expires_ms <= ? LIMIT 100)`), now.UnixMilli()); err != nil {
		return "", time.Time{}, errors.New("cannot clean up browser sessions")
	}
	if validBrowserSessionToken(previous) {
		if _, err := tx.ExecContext(ctx, s.querySQL("DELETE FROM browser_sessions WHERE hash = ?"), apiKeyHash(previous)); err != nil {
			return "", time.Time{}, errors.New("cannot rotate browser session")
		}
	}
	if _, err := tx.ExecContext(ctx, s.querySQL(`INSERT INTO browser_sessions
 (hash, parent_key_id, bootstrap_hash, created_ms, expires_ms) VALUES (?, ?, ?, ?, ?)`), apiKeyHash(token), identity.KeyID, bootstrapHash, now.UnixMilli(), expires.UnixMilli()); err != nil {
		return "", time.Time{}, errors.New("cannot create browser session")
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, errors.New("cannot commit browser session")
	}
	return token, expires, nil
}

func (s *MetadataStore) authenticateBrowserSession(ctx context.Context, token, bootstrapToken string, now time.Time) (authenticatedIdentity, bool, error) {
	if !validBrowserSessionToken(token) || bootstrapToken == "" {
		return authenticatedIdentity{}, false, nil
	}
	var parentID, bootstrapHash, parentName string
	err := s.db.QueryRowContext(ctx, s.querySQL(`SELECT session.parent_key_id, session.bootstrap_hash, COALESCE(parent.name, '')
 FROM browser_sessions AS session LEFT JOIN api_keys AS parent ON parent.id = session.parent_key_id
 WHERE session.hash = ? AND session.expires_ms > ?
 AND (session.parent_key_id = '' OR (parent.id IS NOT NULL AND parent.revoked_at = ''
 AND (parent.expires_ms = 0 OR parent.expires_ms > ?)))`), apiKeyHash(token), now.UnixMilli(), now.UnixMilli()).Scan(&parentID, &bootstrapHash, &parentName)
	if errors.Is(err, sql.ErrNoRows) {
		return authenticatedIdentity{}, false, nil
	}
	if err != nil {
		return authenticatedIdentity{}, false, errors.New("cannot verify browser session")
	}
	if parentID == "" {
		if subtle.ConstantTimeCompare([]byte(bootstrapHash), []byte(apiKeyHash(bootstrapToken))) != 1 {
			return authenticatedIdentity{}, false, nil
		}
		return authenticatedIdentity{Subject: "operator", Name: "Operator", store: s}, true, nil
	}
	return authenticatedIdentity{Subject: "api-key:" + parentID, Name: parentName, KeyID: parentID, store: s}, true, nil
}

func (s *MetadataStore) revokeBrowserSession(ctx context.Context, token string) error {
	if !validBrowserSessionToken(token) {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, s.querySQL("DELETE FROM browser_sessions WHERE hash = ?"), apiKeyHash(token)); err != nil {
		return errors.New("cannot revoke browser session")
	}
	return nil
}

func (h *serverHandler) browserSessionRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		serverMethod(w, "POST, DELETE")
		return
	}
	if !h.browserSessionAvailable() {
		serverJSON(w, http.StatusConflict, map[string]string{"error": "Browser sessions require control-plane authentication and metadata storage"})
		return
	}
	if !h.allowedBrowserSessionOrigin(r) {
		serverJSON(w, http.StatusForbidden, map[string]string{"error": "Browser session origin is not allowed"})
		return
	}
	store := h.authStore.(*MetadataStore)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	cookie := &http.Cookie{Name: browserSessionCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: h.secureBrowserCookies(r)}
	if r.Method == http.MethodDelete {
		if err := store.revokeBrowserSession(ctx, browserCookieToken(r)); err != nil {
			apiKeyStorageError(w)
			return
		}
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0).UTC()
		http.SetCookie(w, cookie)
		serverJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	// A browser cookie is never sufficient to establish or renew a session.
	// Require an explicit bearer credential, including for session rotation.
	supplied, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || supplied == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		serverJSON(w, http.StatusUnauthorized, map[string]string{"error": "API key or team token is required"})
		return
	}
	identity, valid, err := h.authenticate(r.WithContext(ctx))
	if err != nil {
		apiKeyStorageError(w)
		return
	}
	if !valid {
		w.Header().Set("WWW-Authenticate", "Bearer")
		serverJSON(w, http.StatusUnauthorized, map[string]string{"error": "API key or team token was not accepted"})
		return
	}
	now := time.Now()
	token, expires, err := store.createBrowserSession(ctx, identity, h.options.AuthToken, browserCookieToken(r), now)
	if errors.Is(err, errBrowserSessionInvalid) {
		serverJSON(w, http.StatusUnauthorized, map[string]string{"error": "API key or team token was not accepted"})
		return
	}
	if err != nil {
		apiKeyStorageError(w)
		return
	}
	cookie.Value = token
	cookie.Expires = expires.UTC()
	cookie.MaxAge = int(expires.Sub(now).Seconds())
	if cookie.MaxAge < 1 {
		cookie.MaxAge = 1
	}
	http.SetCookie(w, cookie)
	serverJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}
