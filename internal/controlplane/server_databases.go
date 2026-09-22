package controlplane

// DatabaseHandler keeps connection profiles outside the storage engine. Each
// connection runs the same Service and HTTP routes against its own Redis client.
import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type databaseProfile struct {
	RedisURL      string `json:"redis_url,omitempty"`
	Revision      string `json:"config_revision,omitempty"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	RedisAddr     string `json:"redis_addr"`
	RedisUsername string `json:"redis_username,omitempty"`
	RedisPassword string `json:"redis_password,omitempty"`
	RedisDB       int    `json:"redis_db"`
	RedisTLS      bool   `json:"redis_tls"`
}

type DatabaseHandler struct {
	root      *serverHandler
	mu        sync.RWMutex
	handlers  map[string]*serverHandler
	profiles  []databaseProfile
	metadata  *MetadataStore
	defaultID string
	revision  int64
	closed    bool
	leases    map[*serverHandler]*databaseLease
}

// NewMetadataDatabaseHandler keeps control-plane identity and configuration
// independent of its managed Redis connections. Redis availability is not a
// prerequisite for startup or for repairing a saved connection.
func NewMetadataDatabaseHandler(metadata *MetadataStore, options HandlerOptions, legacyJSONFile, initialRedisURL string) (*DatabaseHandler, error) {
	if metadata == nil {
		return nil, errors.New("metadata storage is required")
	}
	ctx := context.Background()
	profiles, defaultID, initialized, revision, err := metadata.LoadProfilesSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	if !initialized {
		profiles, defaultID, err = initialDatabaseProfiles(legacyJSONFile, initialRedisURL)
		if err != nil {
			return nil, err
		}
		for i := range profiles {
			if profiles[i].Revision == "" {
				profiles[i].Revision, err = randomManagementToken()
				if err != nil {
					return nil, errors.New("cannot generate database revision")
				}
			}
		}
	}
	root := NewHandler(nil, options).(*serverHandler)
	root.authStore = metadata
	h := &DatabaseHandler{root: root, metadata: metadata, handlers: map[string]*serverHandler{}, leases: map[*serverHandler]*databaseLease{}}
	root.registry = h
	fail := func(err error) (*DatabaseHandler, error) { _ = h.Close(); return nil, err }
	if err := h.installSnapshotLocked(profiles, defaultID, revision); err != nil {
		return fail(err)
	}
	if !initialized {
		nextRevision, err := metadata.SaveProfilesRevision(ctx, h.profiles, defaultID, revision)
		if errors.Is(err, ErrMetadataConflict) {
			// Another instance initialized the catalog first. Its complete snapshot
			// wins; never overwrite it with this instance's startup configuration.
			profiles, defaultID, initialized, revision, err = metadata.LoadProfilesSnapshot(ctx)
			if err == nil && !initialized {
				err = errors.New("metadata initialization did not complete")
			}
			if err == nil {
				err = h.installSnapshotLocked(profiles, defaultID, revision)
			}
		} else if err == nil {
			h.revision = nextRevision
		}
		if err != nil {
			return fail(err)
		}
	}
	return h, nil
}

func initialDatabaseProfiles(filename, initialRedisURL string) ([]databaseProfile, string, error) {
	profiles := []databaseProfile{}
	if filename != "" {
		raw, err := os.ReadFile(filename)
		if err == nil {
			if json.Unmarshal(raw, &profiles) != nil {
				return nil, "", errors.New("invalid legacy database configuration")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, "", errors.New("cannot read legacy database configuration")
		}
	}
	defaultID := ""
	for _, p := range profiles {
		if p.ID == "local" {
			defaultID = p.ID
		}
	}
	// A saved edit of the old default wins over the old startup connection.
	if defaultID == "" && initialRedisURL != "" {
		opts, err := redis.ParseURL(initialRedisURL)
		if err != nil {
			return nil, "", errors.New("invalid initial Redis connection")
		}
		profiles = append([]databaseProfile{{ID: "local", Name: "Redis", RedisURL: initialRedisURL, RedisAddr: opts.Addr, RedisUsername: opts.Username, RedisPassword: opts.Password, RedisDB: opts.DB, RedisTLS: opts.TLSConfig != nil}}, profiles...)
		defaultID = "local"
	}
	if defaultID == "" && len(profiles) > 0 {
		defaultID = profiles[0].ID
		for _, p := range profiles {
			if p.ID < defaultID {
				defaultID = p.ID
			}
		}
	}
	return profiles, defaultID, nil
}

func (h *DatabaseHandler) defaultDatabaseID() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.defaultID
}

func validDatabaseID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (p databaseProfile) client() (*redis.Client, string, error) {
	if strings.TrimSpace(p.Name) == "" || len(p.Name) > 128 || len(p.Description) > 2048 {
		return nil, "", errors.New("provide a database name (up to 128 characters) and description up to 2048 characters")
	}
	if p.RedisDB < 0 {
		return nil, "", errors.New("Redis database index must be a nonnegative integer")
	}
	u := &url.URL{}
	if p.RedisURL != "" {
		parsed, err := url.Parse(p.RedisURL)
		if err != nil {
			return nil, "", errors.New("invalid Redis connection settings")
		}
		u = parsed
	}
	query := u.Query()
	if u.Scheme == "unix" && p.RedisAddr == u.Path {
		if p.RedisTLS {
			return nil, "", errors.New("TLS is not supported for Unix socket connections")
		}
		query.Set("db", strconv.Itoa(p.RedisDB))
	} else {
		host, port, err := net.SplitHostPort(p.RedisAddr)
		n, portErr := strconv.Atoi(port)
		if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 || strings.ContainsAny(host, "/@?#\\ \t\r\n") {
			return nil, "", errors.New("Redis endpoint must be a host:port address")
		}
		u.Scheme = "redis"
		if p.RedisTLS {
			u.Scheme = "rediss"
		}
		u.Host = p.RedisAddr
		u.Path = "/" + strconv.Itoa(p.RedisDB)
		u.RawPath = ""
		query.Del("db")
	}
	u.User = nil
	if p.RedisUsername != "" || p.RedisPassword != "" {
		u.User = url.UserPassword(p.RedisUsername, p.RedisPassword)
	}
	if !p.RedisTLS {
		query.Del("skip_verify")
	}
	if p.RedisURL == "" {
		query.Set("dial_timeout", "3s")
		query.Set("read_timeout", "3s")
		query.Set("write_timeout", "3s")
		query.Set("max_retries", "-1")
	}
	u.RawQuery = query.Encode()
	options, err := redis.ParseURL(u.String())
	if err != nil {
		return nil, "", errors.New("invalid Redis connection settings")
	}
	options.ContextTimeoutEnabled = true
	return redis.NewClient(options), u.String(), nil
}

func (h *DatabaseHandler) newHandler(p databaseProfile, rdb *redis.Client, connection, defaultID string) *serverHandler {
	options := h.root.options
	options.DatabaseID, options.DatabaseName, options.DatabaseDescription = p.ID, p.Name, p.Description
	options.AdditionalDatabase, options.RedisURL = p.ID != defaultID, connection
	options.DatabaseRevision = p.Revision
	options.UI = nil
	handler := NewHandler(NewService(NewStore(rdb)), options).(*serverHandler)
	handler.authStore = h.root.authStore
	handler.databaseRegistry = h
	return handler
}

func (h *DatabaseHandler) lookup(id string) *serverHandler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.handlers[id]
}
func (h *serverHandler) databaseHandlers() []*serverHandler {
	if h.registry == nil {
		return []*serverHandler{h}
	}
	h.registry.mu.RLock()
	defer h.registry.mu.RUnlock()
	result := make([]*serverHandler, 0, len(h.registry.handlers))
	for _, handler := range h.registry.handlers {
		result = append(result, handler)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].options.DatabaseID == result[j].options.DatabaseID {
			return false
		}
		if result[i].options.DatabaseID == h.registry.defaultID {
			return true
		}
		if result[j].options.DatabaseID == h.registry.defaultID {
			return false
		}
		return result[i].options.DatabaseID < result[j].options.DatabaseID
	})
	return result
}

func (h *DatabaseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A scoped base URL lets the existing CLI and daemon use /v1 endpoints for
	// an added connection, including credential bootstrap and session heartbeats.
	if strings.HasPrefix(r.URL.Path, "/databases/") {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/databases/"), "/", 2)
		if len(parts) == 2 && validDatabaseID(parts[0]) && strings.HasPrefix(parts[1], "v1/") {
			copy := r.Clone(r.Context())
			copy.URL.Path = "/v1/databases/" + parts[0] + "/" + strings.TrimPrefix(parts[1], "v1/")
			copy.URL.RawPath = ""
			h.root.ServeHTTP(w, copy)
			return
		}
	}
	h.root.ServeHTTP(w, r)
}

func (h *DatabaseHandler) duplicate(p databaseProfile, options *redis.Options) bool {
	for id, handler := range h.handlers {
		if id == p.ID {
			continue
		}
		existing := handler.service.store.rdb.Options()
		if strings.EqualFold(strings.TrimSpace(handler.options.DatabaseName), p.Name) || existing.Network == options.Network && strings.EqualFold(existing.Addr, options.Addr) && existing.DB == options.DB {
			return true
		}
	}
	return false
}

func (h *DatabaseHandler) saveProfilesLocked(ctx context.Context, profiles []databaseProfile, defaultID string) error {
	revision, err := h.metadata.SaveProfilesRevision(ctx, profiles, defaultID, h.revision)
	if err == nil {
		h.revision = revision
	}
	return err
}

func databaseSaveError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrMetadataConflict) {
		serverJSON(w, http.StatusConflict, map[string]string{"error": "Database connections changed; reload and try again", "code": "stale_database_registry"})
		return
	}
	serverError(w, errors.New("cannot save database configuration"))
}

func (h *DatabaseHandler) databasesRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items := []any{}
		handlers, release := h.root.acquireDatabaseHandlers()
		defer release()
		results := runDatabaseQueries(r.Context(), handlers, func(ctx context.Context, handler *serverHandler) (map[string]any, error) {
			return handler.databaseRecord(ctx), nil
		})
		for _, result := range results {
			items = append(items, result.Value)
		}
		serverJSON(w, 200, map[string]any{"items": items, "default_database_id": h.defaultDatabaseID()})
	case http.MethodPost:
		// IDs are generated by the server; unknown fields and malformed input use
		// a fixed error so JSON parser diagnostics cannot expose pasted credentials.
		var input struct {
			Name          string `json:"name"`
			Description   string `json:"description"`
			RedisAddr     string `json:"redis_addr"`
			RedisUsername string `json:"redis_username"`
			RedisPassword string `json:"redis_password"`
			RedisDB       int    `json:"redis_db"`
			RedisTLS      bool   `json:"redis_tls"`
		}
		if serverDecode(w, r, &input) != nil {
			serverError(w, errors.New("invalid database connection settings"))
			return
		}
		p := databaseProfile{Name: strings.TrimSpace(input.Name), Description: strings.TrimSpace(input.Description), RedisAddr: strings.TrimSpace(input.RedisAddr), RedisUsername: input.RedisUsername, RedisPassword: input.RedisPassword, RedisDB: input.RedisDB, RedisTLS: input.RedisTLS}
		client, connection, err := p.client()
		if err != nil {
			serverError(w, err)
			return
		}
		retained := false
		defer func() {
			if !retained {
				_ = client.Close()
			}
		}()
		h.mu.RLock()
		duplicate := h.duplicate(p, client.Options())
		h.mu.RUnlock()
		if duplicate {
			serverJSON(w, 409, map[string]string{"error": "A database with this name or endpoint and index is already configured"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err = client.Ping(ctx).Err()
		cancel()
		if err != nil {
			serverError(w, errors.New("Cannot connect to Redis; check the endpoint, credentials, database index and TLS settings"))
			return
		}
		token, err := randomManagementToken()
		if err != nil {
			serverError(w, errors.New("cannot generate database ID"))
			return
		}
		p.ID = "db_" + token[:16]
		p.Revision, err = randomManagementToken()
		if err != nil {
			serverError(w, errors.New("cannot generate database revision"))
			return
		}
		p.RedisURL = connection
		handler := h.newHandler(p, client, connection, h.defaultDatabaseID())
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			serverError(w, errors.New("control plane is shutting down"))
			return
		}
		if h.duplicate(p, client.Options()) {
			h.mu.Unlock()
			serverJSON(w, 409, map[string]string{"error": "A database with this name or endpoint and index is already configured"})
			return
		}
		next := append(append([]databaseProfile(nil), h.profiles...), p)
		defaultID := h.defaultID
		if defaultID == "" {
			defaultID = p.ID
		}
		if err := h.saveProfilesLocked(r.Context(), next, defaultID); err != nil {
			h.mu.Unlock()
			databaseSaveError(w, err)
			return
		}
		h.profiles, h.defaultID, retained = next, defaultID, true
		h.swapHandlerLocked(p.ID, handler)
		h.mu.Unlock()
		serverJSON(w, http.StatusCreated, handler.databaseSummary())
	default:
		serverMethod(w, "GET, POST")
	}
}

func (h *serverHandler) databaseRecord(ctx context.Context) map[string]any {
	record, err := h.database(ctx)
	if err == nil {
		return record
	}
	record = h.databaseSummary()
	record["connection_error"] = "Redis is unavailable"
	return record
}

func (h *DatabaseHandler) workspacesRoute(w http.ResponseWriter, r *http.Request) {
	items := []any{}
	unavailable := []string{}
	handlers, release := h.root.acquireDatabaseHandlers()
	defer release()
	results := runDatabaseQueries(r.Context(), handlers, func(ctx context.Context, handler *serverHandler) ([]any, error) { return handler.workspaceRecords(ctx) })
	for i, result := range results {
		if result.Err != nil {
			unavailable = append(unavailable, handlers[i].options.DatabaseID)
			continue
		}
		items = append(items, result.Value...)
	}
	serverJSON(w, 200, map[string]any{"items": items, "unavailable_database_ids": unavailable})
}
func (h *serverHandler) workspaceRecords(ctx context.Context) ([]any, error) {
	metas, err := h.service.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(metas))
	for _, meta := range metas {
		item, err := h.workspace(ctx, meta, false)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
func (h *DatabaseHandler) agentsRoute(w http.ResponseWriter, r *http.Request) {
	items := []ManagedSession{}
	unavailable := []string{}
	handlers, release := h.root.acquireDatabaseHandlers()
	defer release()
	results := runDatabaseQueries(r.Context(), handlers, func(ctx context.Context, handler *serverHandler) ([]ManagedSession, error) {
		return handler.sessions(ctx, "")
	})
	for i, result := range results {
		if result.Err != nil {
			unavailable = append(unavailable, handlers[i].options.DatabaseID)
			continue
		}
		items = append(items, result.Value...)
	}
	serverJSON(w, 200, map[string]any{"items": items, "unavailable_database_ids": unavailable})
}

var _ http.Handler = (*DatabaseHandler)(nil)

// Bound total aggregate latency, including when several connections are down.
// Each worker only owns its result slot; the snapshot's handlers are immutable.
type databaseQueryResult[T any] struct {
	Value T
	Err   error
}

func runDatabaseQueries[T any](ctx context.Context, handlers []*serverHandler, query func(context.Context, *serverHandler) (T, error)) []databaseQueryResult[T] {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	results := make([]databaseQueryResult[T], len(handlers))
	var wg sync.WaitGroup
	for i, handler := range handlers {
		wg.Add(1)
		go func(i int, handler *serverHandler) {
			defer wg.Done()
			results[i].Value, results[i].Err = query(ctx, handler)
		}(i, handler)
	}
	wg.Wait()
	return results
}
