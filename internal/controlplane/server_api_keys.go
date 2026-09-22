package controlplane

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type authenticatedIdentity struct {
	Subject string
	Name    string
	KeyID   string
	store   apiKeyStore
}
type authenticatedIdentityContextKey struct{}

func requestIdentity(ctx context.Context) authenticatedIdentity {
	identity, _ := ctx.Value(authenticatedIdentityContextKey{}).(authenticatedIdentity)
	return identity
}
func (h *serverHandler) authenticate(r *http.Request) (authenticatedIdentity, bool, error) {
	// Internal database dispatch retains the root's decision, and never consults
	// the selected database for authentication records.
	if identity := requestIdentity(r.Context()); identity.store != nil && identity.store == h.authStore {
		return identity, true, nil
	}
	operator := authenticatedIdentity{Subject: "operator", Name: "Operator", store: h.authStore}
	if h.options.AuthToken == "" {
		return operator, true, nil
	}
	supplied, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || supplied == "" {
		return authenticatedIdentity{}, false, nil
	}
	expectedHash, suppliedHash := sha256.Sum256([]byte(h.options.AuthToken)), sha256.Sum256([]byte(supplied))
	if subtle.ConstantTimeCompare(expectedHash[:], suppliedHash[:]) == 1 {
		return operator, true, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	key, valid, err := h.authStore.authenticateAPIKey(ctx, supplied, time.Now())
	if !valid || err != nil {
		return authenticatedIdentity{}, false, err
	}
	return authenticatedIdentity{Subject: "api-key:" + key.ID, Name: key.Name, KeyID: key.ID, store: h.authStore}, true, nil
}
func (h *serverHandler) apiKeysRoute(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" && r.Method == http.MethodGet {
		if h.options.AuthToken == "" {
			serverJSON(w, 200, map[string]any{"keys": []APIKey{}, "enabled": false})
			return
		}
		limit := 100
		var err error
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
		}
		cursor := r.URL.Query().Get("cursor")
		if err != nil || limit < 1 || limit > 1000 || cursor != "" && !validAPIKeyID(cursor) {
			serverJSON(w, 400, map[string]string{"error": "limit must be between 1 and 1000 and cursor must be a key ID"})
			return
		}
		keys, next, err := h.authStore.listAPIKeys(r.Context(), cursor, limit, time.Now())
		if err != nil {
			apiKeyStorageError(w)
			return
		}
		result := map[string]any{"keys": keys, "enabled": true}
		if next != "" {
			result["next_cursor"] = next
		}
		serverJSON(w, 200, result)
		return
	}
	if id == "" && r.Method != http.MethodPost {
		serverMethod(w, "GET, POST")
		return
	}
	if id != "" && r.Method != http.MethodDelete {
		serverMethod(w, "DELETE")
		return
	}
	if h.options.AuthToken == "" {
		serverJSON(w, 409, map[string]string{"error": "Configure AFS_CONTROL_PLANE_TOKEN on the server before managing API keys"})
		return
	}
	if id == "" {
		var input APIKeyCreateInput
		if err := serverDecode(w, r, &input); err != nil {
			serverJSON(w, 400, map[string]string{"error": "invalid API key request"})
			return
		}
		key, err := validateAPIKeyInput(input, time.Now())
		if err != nil {
			serverError(w, err)
			return
		}
		key, token, err := h.authStore.createAPIKey(r.Context(), key)
		if err != nil {
			apiKeyStorageError(w)
			return
		}
		serverJSON(w, 200, map[string]any{"key": key, "token": token})
		return
	}
	if !validAPIKeyID(id) {
		serverError(w, os.ErrNotExist)
		return
	}
	key, err := h.authStore.revokeAPIKey(r.Context(), id, time.Now())
	if errors.Is(err, os.ErrNotExist) {
		serverError(w, err)
		return
	}
	if err != nil {
		apiKeyStorageError(w)
		return
	}
	serverJSON(w, 200, map[string]any{"key": key})
}
func apiKeyStorageError(w http.ResponseWriter) {
	serverJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "API key storage is unavailable"})
}
