package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func profileFromHandler(h *serverHandler) databaseProfile {
	opts := h.service.store.rdb.Options()
	// The startup CLI may override URL dial/retry settings. Persist their
	// effective values so a metadata-only edit cannot silently change them.
	connection := h.options.RedisURL
	if connection != "" {
		if u, err := url.Parse(connection); err == nil {
			query := u.Query()
			query.Set("dial_timeout", opts.DialTimeout.String())
			retries := opts.MaxRetries
			if retries == 0 {
				retries = -1
			}
			query.Set("max_retries", strconv.Itoa(retries))
			u.RawQuery = query.Encode()
			connection = u.String()
		}
	}
	return databaseProfile{ID: h.options.DatabaseID, Name: h.options.DatabaseName, Description: h.options.DatabaseDescription, RedisAddr: opts.Addr, RedisUsername: opts.Username, RedisPassword: opts.Password, RedisDB: opts.DB, RedisTLS: opts.TLSConfig != nil, RedisURL: connection, Revision: h.options.DatabaseRevision}
}

func (h *DatabaseHandler) updateDatabaseRoute(w http.ResponseWriter, r *http.Request, id string) {
	previous, release := h.acquire(id)
	defer release()
	if previous == nil {
		serverError(w, os.ErrNotExist)
		return
	}
	var input struct {
		Name          string  `json:"name"`
		Description   string  `json:"description"`
		RedisAddr     string  `json:"redis_addr"`
		RedisUsername string  `json:"redis_username"`
		RedisPassword *string `json:"redis_password"`
		RedisDB       int     `json:"redis_db"`
		RedisTLS      bool    `json:"redis_tls"`
		Revision      string  `json:"config_revision"`
	}
	if serverDecode(w, r, &input) != nil {
		serverError(w, errors.New("invalid database connection settings"))
		return
	}
	conflict := func() {
		serverJSON(w, http.StatusConflict, map[string]string{"error": "Database settings changed; reopen the settings and try again", "code": "stale_database_settings"})
	}
	if input.Revision == "" || input.Revision != previous.options.DatabaseRevision {
		conflict()
		return
	}
	profile := profileFromHandler(previous)
	profile.Name, profile.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	profile.RedisAddr, profile.RedisUsername = strings.TrimSpace(input.RedisAddr), input.RedisUsername
	profile.RedisDB, profile.RedisTLS = input.RedisDB, input.RedisTLS
	if input.RedisPassword != nil {
		profile.RedisPassword = *input.RedisPassword
	}
	candidate, connection, err := profile.client()
	if err != nil {
		serverError(w, err)
		return
	}
	retained := false
	defer func() {
		if !retained {
			_ = candidate.Close()
		}
	}()
	h.mu.RLock()
	duplicate := h.duplicate(profile, candidate.Options())
	h.mu.RUnlock()
	if duplicate {
		serverJSON(w, http.StatusConflict, map[string]string{"error": "A database with this name or endpoint and index is already configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	err = candidate.Ping(ctx).Err()
	cancel()
	if err != nil {
		serverError(w, errors.New("Cannot connect to Redis; check the endpoint, credentials, database index and TLS settings"))
		return
	}
	profile.Revision, err = randomManagementToken()
	if err != nil {
		serverError(w, errors.New("cannot generate database revision"))
		return
	}
	profile.RedisURL = connection
	replacement := h.newHandler(profile, candidate, connection)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		serverError(w, errors.New("control plane is shutting down"))
		return
	}
	if h.handlers[id] != previous {
		h.mu.Unlock()
		conflict()
		return
	}
	if h.duplicate(profile, candidate.Options()) {
		h.mu.Unlock()
		serverJSON(w, http.StatusConflict, map[string]string{"error": "A database with this name or endpoint and index is already configured"})
		return
	}
	next := append([]databaseProfile(nil), h.profiles...)
	found := false
	for i := range next {
		if next[i].ID == id {
			next[i] = profile
			found = true
			break
		}
	}
	if !found {
		next = append(next, profile)
	}
	if err := h.save(next); err != nil {
		h.mu.Unlock()
		serverError(w, errors.New("cannot save database configuration; check server file permissions"))
		return
	}
	h.profiles, retained = next, true
	h.swapHandlerLocked(id, replacement)
	h.mu.Unlock()
	serverJSON(w, http.StatusOK, replacement.databaseSummary())
}
