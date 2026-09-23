package controlplane

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"
)

const cliLoginSchema = `CREATE TABLE IF NOT EXISTS cli_login_requests (
 id TEXT PRIMARY KEY NOT NULL,
 device_hash TEXT UNIQUE NOT NULL,
 name TEXT NOT NULL,
 user_code TEXT NOT NULL,
 challenge TEXT NOT NULL,
 expires_ms BIGINT NOT NULL,
 next_poll_ms BIGINT NOT NULL DEFAULT 0,
 status TEXT NOT NULL DEFAULT 'pending',
 approver_key_id TEXT NOT NULL DEFAULT '',
 approver_hash TEXT NOT NULL DEFAULT '',
 key_expires_ms BIGINT NOT NULL DEFAULT 0
)`

const (
	cliLoginLifetime       = 10 * time.Minute
	cliLoginPollInterval   = 2
	cliLoginMaxOutstanding = 256
)

var errCLILoginCapacity = errors.New("too many CLI sign-in requests; try again later")

// CLILoginStart contains the CLI-only device secret and a separate public
// verification link. Only the public request ID belongs in a browser URL.
type CLILoginStart struct {
	ID               string `json:"id"`
	DeviceCode       string `json:"device_code"`
	UserCode         string `json:"user_code"`
	ExpiresAt        string `json:"expires_at"`
	Interval         int    `json:"interval"`
	VerificationPath string `json:"verification_path"`
}

type CLILoginPoll struct {
	Status   string  `json:"status"`
	Interval int     `json:"interval,omitempty"`
	Token    string  `json:"token,omitempty"`
	Key      *APIKey `json:"key,omitempty"`
}

type CLIAuthConfig struct {
	Enabled         bool `json:"enabled"`
	BrowserLogin    bool `json:"browser_login"`
	BrowserSessions bool `json:"browser_sessions"`
	Authenticated   bool `json:"authenticated"`
}

type cliLoginRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UserCode  string `json:"user_code"`
	ExpiresAt string `json:"expires_at"`
	Status    string `json:"status"`

	challenge, approverKeyID, approverHash string
	expiresMS, nextPollMS, keyExpiresMS    int64
}

const cliLoginColumns = "id, name, user_code, challenge, expires_ms, next_poll_ms, status, approver_key_id, approver_hash, key_expires_ms"

func scanCLILogin(row apiKeyScanner) (cliLoginRequest, error) {
	var request cliLoginRequest
	err := row.Scan(&request.ID, &request.Name, &request.UserCode, &request.challenge, &request.expiresMS, &request.nextPollMS, &request.Status, &request.approverKeyID, &request.approverHash, &request.keyExpiresMS)
	request.ExpiresAt = serverTime(time.UnixMilli(request.expiresMS))
	return request, err
}

func validCLILoginID(id string) bool {
	if len(id) != 36 || !strings.HasPrefix(id, "cli_") || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id[4:])
	return err == nil
}

func validCLILoginSecret(secret string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == secret
}

func validCLILoginVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, ch := range verifier {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("-._~", ch)) {
			return false
		}
	}
	return true
}

func (h *serverHandler) cliLoginAvailable() bool {
	store, ok := h.authStore.(*MetadataStore)
	return h.options.AuthToken != "" && ok && store != nil
}

func cliLoginJSONInput(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		serverJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := serverDecode(w, r, target); err != nil {
		serverJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid CLI sign-in request"})
		return false
	}
	return true
}

func cliLoginStorageError(w http.ResponseWriter) {
	serverJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "CLI sign-in storage is unavailable"})
}

// cliLoginPublicRoute never accepts an approval; knowledge of the request URL
// alone cannot create a credential or retrieve the device secret.
func (h *serverHandler) cliLoginPublicRoute(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/auth/cli/start" && path != "/auth/cli/token" {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		serverMethod(w, "POST")
		return true
	}
	if !h.cliLoginAvailable() {
		serverJSON(w, http.StatusConflict, map[string]string{"error": "browser CLI sign-in is unavailable; configure the team token and metadata storage"})
		return true
	}
	store := h.authStore.(*MetadataStore)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if path == "/auth/cli/start" {
		var input struct {
			Name      string `json:"name"`
			Challenge string `json:"code_challenge"`
		}
		if !cliLoginJSONInput(w, r, &input) {
			return true
		}
		key, err := validateAPIKeyInput(APIKeyCreateInput{Name: input.Name}, time.Now())
		if err != nil || !validCLILoginSecret(input.Challenge) {
			serverJSON(w, 400, map[string]string{"error": "provide a client name and valid S256 code challenge"})
			return true
		}
		started, err := store.startCLILogin(ctx, key.Name, input.Challenge, time.Now())
		if errors.Is(err, errCLILoginCapacity) {
			w.Header().Set("Retry-After", "60")
			serverJSON(w, http.StatusTooManyRequests, map[string]string{"error": errCLILoginCapacity.Error()})
		} else if err != nil {
			cliLoginStorageError(w)
		} else {
			serverJSON(w, 200, started)
		}
		return true
	}
	var input struct {
		DeviceCode string `json:"device_code"`
		Verifier   string `json:"code_verifier"`
	}
	if !cliLoginJSONInput(w, r, &input) {
		return true
	}
	if !validCLILoginSecret(input.DeviceCode) || !validCLILoginVerifier(input.Verifier) {
		serverJSON(w, 400, map[string]string{"error": "invalid CLI sign-in proof"})
		return true
	}
	result, err := store.exchangeCLILogin(ctx, input.DeviceCode, input.Verifier, apiKeyHash(h.options.AuthToken), time.Now())
	if err != nil {
		cliLoginStorageError(w)
	} else {
		serverJSON(w, 200, result)
	}
	return true
}

func (h *serverHandler) cliLoginAuthenticatedRoute(w http.ResponseWriter, r *http.Request, path string) bool {
	const prefix = "/auth/cli/requests/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if !h.cliLoginAvailable() {
		serverJSON(w, 409, map[string]string{"error": "browser CLI sign-in is unavailable"})
		return true
	}
	id := strings.TrimPrefix(path, prefix)
	if !validCLILoginID(id) {
		serverJSON(w, 404, map[string]string{"error": "CLI sign-in request not found"})
		return true
	}
	store := h.authStore.(*MetadataStore)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if r.Method == http.MethodGet {
		request, err := scanCLILogin(store.db.QueryRowContext(ctx, store.querySQL("SELECT "+cliLoginColumns+" FROM cli_login_requests WHERE id = ?"), id))
		if errors.Is(err, sql.ErrNoRows) {
			serverJSON(w, 404, map[string]string{"error": "CLI sign-in request not found or expired"})
		} else if err != nil {
			cliLoginStorageError(w)
		} else {
			if request.expiresMS <= time.Now().UnixMilli() {
				request.Status = "expired"
			}
			serverJSON(w, 200, request)
		}
		return true
	}
	if r.Method != http.MethodPost {
		serverMethod(w, "GET, POST")
		return true
	}
	var input struct {
		Decision string `json:"decision"`
		UserCode string `json:"user_code"`
	}
	if !cliLoginJSONInput(w, r, &input) {
		return true
	}
	if input.Decision != "approve" && input.Decision != "deny" {
		serverJSON(w, 400, map[string]string{"error": "decision must be approve or deny"})
		return true
	}
	status, err := store.decideCLILogin(ctx, id, input.UserCode, input.Decision, requestIdentity(r.Context()), apiKeyHash(h.options.AuthToken), time.Now())
	if err != nil {
		cliLoginStorageError(w)
	} else if status == "invalid_code" {
		serverJSON(w, 400, map[string]string{"error": "confirmation code does not match the CLI"})
	} else if status == "expired" {
		serverJSON(w, 410, map[string]string{"error": "CLI sign-in request expired; start again"})
	} else if status == "unauthorized" {
		serverJSON(w, 401, map[string]string{"error": "administrator authentication required"})
	} else if status == "conflict" {
		serverJSON(w, 409, map[string]string{"error": "CLI sign-in request already decided"})
	} else {
		serverJSON(w, 200, map[string]string{"status": status})
	}
	return true
}

func (s *MetadataStore) startCLILogin(ctx context.Context, name, challenge string, now time.Time) (CLILoginStart, error) {
	var random [56]byte
	if _, err := rand.Read(random[:]); err != nil {
		return CLILoginStart{}, err
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var code [9]byte
	for i := 0; i < 8; i++ {
		j := i
		if i >= 4 {
			j++
		}
		code[j] = alphabet[int(random[48+i])%len(alphabet)]
	}
	code[4] = '-'
	result := CLILoginStart{ID: "cli_" + hex.EncodeToString(random[:16]), DeviceCode: base64.RawURLEncoding.EncodeToString(random[16:48]), UserCode: string(code[:]), ExpiresAt: serverTime(now.Add(cliLoginLifetime)), Interval: cliLoginPollInterval}
	result.VerificationPath = "/connect-cli?request=" + result.ID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CLILoginStart{}, err
	}
	defer tx.Rollback()
	// This persisted row serializes capacity decisions across all Postgres
	// instances and works with SQLite's single writer as well.
	if _, err = tx.ExecContext(ctx, s.querySQL("INSERT INTO metadata_settings (name, value) VALUES ('cli_login_lock', '0') ON CONFLICT(name) DO NOTHING")); err != nil {
		return CLILoginStart{}, err
	}
	if _, err = tx.ExecContext(ctx, s.querySQL("UPDATE metadata_settings SET value = value WHERE name = 'cli_login_lock'")); err != nil {
		return CLILoginStart{}, err
	}
	if _, err = tx.ExecContext(ctx, s.querySQL("DELETE FROM cli_login_requests WHERE expires_ms <= ?"), now.UnixMilli()); err != nil {
		return CLILoginStart{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, s.querySQL("SELECT COUNT(*) FROM cli_login_requests")).Scan(&count); err != nil {
		return CLILoginStart{}, err
	}
	if count >= cliLoginMaxOutstanding {
		return CLILoginStart{}, errCLILoginCapacity
	}
	_, err = tx.ExecContext(ctx, s.querySQL("INSERT INTO cli_login_requests (id, device_hash, name, user_code, challenge, expires_ms) VALUES (?, ?, ?, ?, ?, ?)"), result.ID, apiKeyHash(result.DeviceCode), name, result.UserCode, challenge, now.Add(cliLoginLifetime).UnixMilli())
	if err != nil {
		return CLILoginStart{}, err
	}
	if err = tx.Commit(); err != nil {
		return CLILoginStart{}, err
	}
	return result, nil
}

// parentCLILoginExpiry locks a key while checking its authority. A concurrent
// revocation therefore orders before or after the credential's atomic issue.
func (s *MetadataStore) parentCLILoginExpiry(ctx context.Context, tx *sql.Tx, id string, now time.Time) (int64, bool, error) {
	var expires int64
	err := tx.QueryRowContext(ctx, s.querySQL("UPDATE api_keys SET name = name WHERE id = ? AND revoked_at = '' AND (expires_ms = 0 OR expires_ms > ?) RETURNING expires_ms"), id, now.UnixMilli()).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return expires, err == nil, err
}

func (s *MetadataStore) decideCLILogin(ctx context.Context, id, code, decision string, identity authenticatedIdentity, bootstrapHash string, now time.Time) (string, error) {
	if identity.store != s || identity.Subject == "" {
		return "unauthorized", nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	request, err := scanCLILogin(tx.QueryRowContext(ctx, s.querySQL("UPDATE cli_login_requests SET status = status WHERE id = ? RETURNING "+cliLoginColumns), id))
	if errors.Is(err, sql.ErrNoRows) {
		return "expired", nil
	}
	if err != nil {
		return "", err
	}
	if request.expiresMS <= now.UnixMilli() {
		return "expired", nil
	}
	if request.Status != "pending" {
		return "conflict", nil
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(request.UserCode)) != 1 {
		return "invalid_code", nil
	}
	expiry := now.Add(30 * 24 * time.Hour).UnixMilli()
	parentHash := ""
	if identity.KeyID != "" {
		parentExpiry, valid, err := s.parentCLILoginExpiry(ctx, tx, identity.KeyID, now)
		if err != nil {
			return "", err
		}
		if !valid {
			return "unauthorized", nil
		}
		if parentExpiry > 0 && parentExpiry < expiry {
			expiry = parentExpiry
		}
	} else if identity.Subject == "operator" {
		parentHash = bootstrapHash
	} else {
		return "unauthorized", nil
	}
	status := "approved"
	if decision == "deny" {
		status = "denied"
	}
	_, err = tx.ExecContext(ctx, s.querySQL("UPDATE cli_login_requests SET status = ?, approver_key_id = ?, approver_hash = ?, key_expires_ms = ? WHERE id = ?"), status, identity.KeyID, parentHash, expiry, id)
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return status, nil
}

func (s *MetadataStore) exchangeCLILogin(ctx context.Context, deviceCode, verifier, bootstrapHash string, now time.Time) (CLILoginPoll, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CLILoginPoll{}, err
	}
	defer tx.Rollback()
	// A row write locks the grant across processes before any decision. The
	// verifier is checked before throttle state changes or grant consumption.
	request, err := scanCLILogin(tx.QueryRowContext(ctx, s.querySQL("UPDATE cli_login_requests SET status = status WHERE device_hash = ? RETURNING "+cliLoginColumns), apiKeyHash(deviceCode)))
	if errors.Is(err, sql.ErrNoRows) {
		return CLILoginPoll{Status: "expired"}, nil
	}
	if err != nil {
		return CLILoginPoll{}, err
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(request.challenge)) != 1 {
		return CLILoginPoll{Status: "denied"}, nil
	}
	if request.expiresMS <= now.UnixMilli() {
		return CLILoginPoll{Status: "expired"}, nil
	}
	if request.Status == "denied" || request.Status == "consumed" {
		return CLILoginPoll{Status: "denied"}, nil
	}
	if request.nextPollMS > now.UnixMilli() {
		return CLILoginPoll{Status: "pending", Interval: cliLoginPollInterval}, nil
	}
	if request.Status == "pending" {
		_, err = tx.ExecContext(ctx, s.querySQL("UPDATE cli_login_requests SET next_poll_ms = ? WHERE id = ?"), now.Add(cliLoginPollInterval*time.Second).UnixMilli(), request.ID)
		if err != nil {
			return CLILoginPoll{}, err
		}
		if err = tx.Commit(); err != nil {
			return CLILoginPoll{}, err
		}
		return CLILoginPoll{Status: "pending", Interval: cliLoginPollInterval}, nil
	}
	if request.Status != "approved" {
		return CLILoginPoll{Status: "denied"}, nil
	}
	valid := request.keyExpiresMS > now.UnixMilli()
	if request.approverKeyID != "" {
		parentExpiry, parentValid, err := s.parentCLILoginExpiry(ctx, tx, request.approverKeyID, now)
		if err != nil {
			return CLILoginPoll{}, err
		}
		valid = valid && parentValid
		if parentExpiry > 0 && parentExpiry < request.keyExpiresMS {
			request.keyExpiresMS = parentExpiry
		}
	} else {
		valid = valid && subtle.ConstantTimeCompare([]byte(request.approverHash), []byte(bootstrapHash)) == 1
	}
	if !valid {
		_, err = tx.ExecContext(ctx, s.querySQL("UPDATE cli_login_requests SET status = 'denied' WHERE id = ?"), request.ID)
		if err != nil {
			return CLILoginPoll{}, err
		}
		if err = tx.Commit(); err != nil {
			return CLILoginPoll{}, err
		}
		return CLILoginPoll{Status: "denied"}, nil
	}
	var random [48]byte
	if _, err = rand.Read(random[:]); err != nil {
		return CLILoginPoll{}, err
	}
	key := APIKey{ID: "key_" + hex.EncodeToString(random[:16]), Name: request.Name, CreatedAt: serverTime(now), ExpiresAt: serverTime(time.UnixMilli(request.keyExpiresMS)), Status: "active"}
	token := "afs_" + key.ID + "." + base64.RawURLEncoding.EncodeToString(random[16:])
	_, err = tx.ExecContext(ctx, s.querySQL(`INSERT INTO api_keys (id, name, created_at, last_used_at, expires_at, expires_ms, revoked_at, hash) VALUES (?, ?, ?, '', ?, ?, '', ?)`), key.ID, key.Name, key.CreatedAt, key.ExpiresAt, request.keyExpiresMS, apiKeyHash(token))
	if err != nil {
		return CLILoginPoll{}, err
	}
	_, err = tx.ExecContext(ctx, s.querySQL("UPDATE cli_login_requests SET status = 'consumed' WHERE id = ?"), request.ID)
	if err != nil {
		return CLILoginPoll{}, err
	}
	if err = tx.Commit(); err != nil {
		return CLILoginPoll{}, err
	}
	return CLILoginPoll{Status: "complete", Token: token, Key: &key}, nil
}

func (c *CLIClient) GetAuthConfig(ctx context.Context) (CLIAuthConfig, error) {
	var result CLIAuthConfig
	err := c.request(ctx, http.MethodGet, "/v1/auth/config", "", nil, &result)
	return result, err
}

func (c *CLIClient) StartCLILogin(ctx context.Context, name, challenge string) (CLILoginStart, error) {
	var result CLILoginStart
	body, _ := json.Marshal(map[string]string{"name": name, "code_challenge": challenge})
	err := c.request(ctx, http.MethodPost, "/v1/auth/cli/start", "application/json", bytes.NewReader(body), &result)
	if err != nil && ctx.Err() == nil {
		err = errors.New("cannot start browser sign-in; check the control-plane URL and availability")
	}
	return result, err
}

func (c *CLIClient) PollCLILogin(ctx context.Context, deviceCode, verifier string) (CLILoginPoll, error) {
	var result CLILoginPoll
	body, _ := json.Marshal(map[string]string{"device_code": deviceCode, "code_verifier": verifier})
	err := c.request(ctx, http.MethodPost, "/v1/auth/cli/token", "application/json", bytes.NewReader(body), &result)
	// Do not reflect server-supplied errors that could echo either secret.
	if err != nil && ctx.Err() == nil {
		err = errors.New("cannot complete browser sign-in; check control-plane availability and try again")
	}
	return result, err
}
