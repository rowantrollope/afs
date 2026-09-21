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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	root     *serverHandler
	mu       sync.RWMutex
	handlers map[string]*serverHandler
	profiles []databaseProfile
	filename string
	lock     *os.File
	closed   bool
	leases   map[*serverHandler]*databaseLease
}

// NewDatabaseHandler loads a private connection file. The startup connection
// remains the default; saved connections never change standalone CLI settings.
// One server owns a file at a time, preventing lost updates between processes.
func NewDatabaseHandler(service *Service, options HandlerOptions, filename string) (*DatabaseHandler, error) {
	if filename == "" {
		return nil, errors.New("database configuration file is required")
	}
	revision, err := randomManagementToken()
	if err != nil {
		return nil, errors.New("cannot generate database revision")
	}
	options.DatabaseRevision = revision
	root := NewHandler(service, options).(*serverHandler)
	primary := *root
	h := &DatabaseHandler{root: root, filename: filename, handlers: map[string]*serverHandler{root.options.DatabaseID: &primary}}
	h.leases = map[*serverHandler]*databaseLease{&primary: {}}
	root.registry = h
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, errors.New("cannot create database configuration directory")
	}
	lock, err := os.OpenFile(filename+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("cannot open database configuration lock")
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("database configuration is already in use by another control plane")
	}
	h.lock = lock
	fail := func(err error) (*DatabaseHandler, error) { h.Close(); return nil, err }
	raw, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return fail(errors.New("cannot read database configuration"))
	}
	if err := json.Unmarshal(raw, &h.profiles); err != nil {
		return fail(errors.New("invalid database configuration"))
	}
	// Load a saved default override before validating additional connections.
	sort.SliceStable(h.profiles, func(i, j int) bool {
		return h.profiles[i].ID == root.options.DatabaseID && h.profiles[j].ID != root.options.DatabaseID
	})
	seen := map[string]bool{}
	for i, profile := range h.profiles {
		if !validDatabaseID(profile.ID) || seen[profile.ID] {
			return fail(errors.New("invalid or duplicate database ID in configuration"))
		}
		seen[profile.ID] = true
		if profile.Revision == "" {
			profile.Revision, err = randomManagementToken()
			if err != nil {
				return fail(errors.New("cannot generate database revision"))
			}
		}
		client, connection, err := profile.client()
		if err != nil {
			return fail(errors.New("invalid database connection in configuration"))
		}
		if h.duplicate(profile, client.Options()) {
			client.Close()
			return fail(errors.New("duplicate database connection or name in configuration"))
		}
		profile.RedisURL = connection
		h.profiles[i] = profile
		h.swapHandlerLocked(profile.ID, h.newHandler(profile, client, connection))
	}
	return h, nil
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

func (h *DatabaseHandler) newHandler(p databaseProfile, rdb *redis.Client, connection string) *serverHandler {
	options := h.root.options
	options.DatabaseID, options.DatabaseName, options.DatabaseDescription = p.ID, p.Name, p.Description
	options.AdditionalDatabase, options.RedisURL = p.ID != h.root.options.DatabaseID, connection
	options.DatabaseRevision = p.Revision
	options.UI = nil
	return NewHandler(NewService(NewStore(rdb)), options).(*serverHandler)
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
		if result[i].options.DatabaseID == h.options.DatabaseID {
			return true
		}
		if result[j].options.DatabaseID == h.options.DatabaseID {
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

func (h *DatabaseHandler) save(profiles []databaseProfile) error {
	raw, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(h.filename), ".afs-databases-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), h.filename)
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
		serverJSON(w, 200, map[string]any{"items": items, "default_database_id": h.root.options.DatabaseID})
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
		handler := h.newHandler(p, client, connection)
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
		if err := h.save(next); err != nil {
			h.mu.Unlock()
			serverError(w, errors.New("cannot save database configuration; check server file permissions"))
			return
		}
		h.profiles, retained = next, true
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
