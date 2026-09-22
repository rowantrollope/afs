package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/managedclient"
	"github.com/rowantrollope/afs/internal/version"
)

func managedSettings(endpoint, token string) managedclient.Settings {
	return managedclient.Settings{URL: endpoint, Token: token}
}

// resolveManagedRedis resolves the server's backend only in memory. Local Redis
// configuration and password overrides belong to standalone operation.
func (a *app) resolveManagedRedis(ctx context.Context) error {
	settings := managedConfig(a.config)
	if settings.URL == "" || a.options.redisURL != "" || a.config.redisFromControlPlane {
		return nil
	}
	remote, err := controlplane.NewCLIClient(settings.URL, settings.Token)
	if err != nil {
		return err
	}
	defer remote.Close()
	connection, err := remote.Connection(ctx)
	if err != nil {
		return err
	}
	if _, err := redis.ParseURL(connection.RedisURL); err != nil {
		return errors.New("control plane returned an invalid Redis connection")
	}
	endpoint, err := remote.DatabaseURL(connection.DatabaseID)
	if err != nil {
		return err
	}
	// connectMount creates its management service before credential bootstrap.
	// Replace that client before workspace lookup, so a concurrent default change
	// cannot pair one database's Redis connection with another's workspace.
	if existing, ok := a.service.(*controlplane.CLIClient); ok && endpoint != settings.URL {
		scoped, err := controlplane.NewCLIClient(endpoint, settings.Token)
		if err != nil {
			return err
		}
		_ = existing.Close()
		a.service = scoped
	}
	settings.URL = endpoint
	a.config.ControlPlane = &settings
	a.config.Redis = connection.RedisURL
	a.config.redisFromControlPlane = true
	return nil
}

// Management remains on HTTP; only mounts open a direct data connection.
func (a *app) connectMount(ctx context.Context) error {
	if err := a.connect(ctx); err != nil {
		return err
	}
	if a.rdb != nil {
		return nil
	}
	if err := a.resolveManagedRedis(ctx); err != nil {
		return err
	}
	rdb := redis.NewClient(buildRedisOptions(a.config, 8))
	ready, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ready).Err(); err != nil {
		_ = rdb.Close()
		return redisConnectionError(a.config)
	}
	a.rdb = rdb
	return nil
}
func managedConfig(cfg config) managedclient.Settings {
	if cfg.ControlPlane == nil {
		return managedclient.Settings{}
	}
	return *cfg.ControlPlane
}
func applyManagedEnvironment(cfg *config) error {
	settings := managedConfig(*cfg)
	if endpoint, ok := os.LookupEnv("AFS_CONTROL_PLANE_URL"); ok {
		endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
		if endpoint != strings.TrimRight(strings.TrimSpace(settings.URL), "/") {
			// Stored credentials belong to the saved server, not an override.
			settings.Token = ""
		}
		settings.URL = endpoint
	}
	if token, ok := os.LookupEnv("AFS_CONTROL_PLANE_TOKEN"); ok {
		settings.Token = token
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	if settings != (managedclient.Settings{}) || cfg.ControlPlane != nil {
		cfg.ControlPlane = &settings
	}
	return nil
}

// Presence identity is persisted with the mount before daemon startup. Explicit
// labels remain descriptive; a managed session gets its own collision-free ID.
func prepareManagedMount(cfg config, rec *mountRecord) error {
	if rec.AgentVersion == "" {
		rec.AgentVersion = version.String()
	}
	if managedConfig(cfg).URL == "" {
		return nil
	}
	rec.ControlPlaneURL = managedConfig(cfg).URL
	if rec.ManagedSession {
		return nil
	}
	rec.SessionName = rec.SessionID
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	rec.SessionID = "sess_" + suffix
	if rec.AgentID == "" {
		rec.AgentID, err = managedAgentID()
		if err != nil {
			return err
		}
	}
	if rec.Label == "" {
		rec.Label, _ = os.Hostname()
	}
	rec.ManagedSession = true
	return nil
}

func managedAgentID() (string, error) {
	// The registry lock serializes callers. Keep the identity separate from
	// workspace contents, tokens, and machine-specific mount IDs.
	path := filepath.Join(baseStateDir(), "agent-id")
	raw, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(raw))
		if id == "" {
			return "", errors.New("managed agent identity is empty")
		}
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	suffix, err := randomSuffix()
	if err != nil {
		return "", err
	}
	id := "agent_" + suffix
	if err := os.MkdirAll(baseStateDir(), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return managedAgentID()
	}
	if err != nil {
		return "", err
	}
	if _, err = f.WriteString(id + "\n"); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return id, nil
}

func managedRegistration(rec mountRecord) managedclient.Registration {
	hostname, _ := os.Hostname()
	kind := rec.Backend
	if kind == "" {
		kind = "sync"
	}
	return managedclient.Registration{SessionID: rec.SessionID, AgentID: rec.AgentID, AgentName: rec.Label, SessionName: rec.SessionName,
		ClientKind: kind, AFSVersion: rec.AgentVersion, Hostname: hostname, OS: runtime.GOOS, LocalPath: rec.LocalPath,
		Label: rec.Label, User: rec.User, Readonly: rec.ReadOnly, WorkspaceID: rec.WorkspaceID, WorkspaceGeneration: rec.Generation}
}
func managedWarning(message string) { fmt.Fprintln(os.Stderr, "afs: management:", message) }

func managementStatusFromRow(row map[string]any) *managedclient.Status {
	if status, ok := row["management"].(*managedclient.Status); ok {
		return status
	}
	if status, ok := row["sync"].(*syncStatus); ok && status != nil {
		return status.Management
	}
	return nil
}

// Daemons receive the resolved management settings in a private bootstrap. Do
// not copy credentials (or a second, potentially different endpoint) into their
// inherited environment.
func daemonEnvironment(configs ...config) []string {
	original := os.Environ()
	environment := make([]string, 0, len(original))
	for _, entry := range original {
		key, _, _ := strings.Cut(entry, "=")
		if len(configs) > 0 && configs[0].redisFromControlPlane && (key == "AFS_REDIS_URL" || key == "AFS_REDIS_PASSWORD") {
			continue
		}
		if key != "AFS_CONTROL_PLANE_URL" && key != "AFS_CONTROL_PLANE_TOKEN" {
			environment = append(environment, entry)
		}
	}
	return environment
}
