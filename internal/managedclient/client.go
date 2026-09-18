// Package managedclient reports optional daemon presence. It never carries file
// traffic or makes a control-plane heartbeat a filesystem lease.
package managedclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Settings struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

func (s Settings) Validate() error {
	if s.URL == "" {
		return nil
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("controlPlane.url must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return errors.New("controlPlane.url requires HTTPS outside loopback")
		}
	}
	return nil
}

type Registration struct {
	SessionID           string `json:"session_id"`
	AgentID             string `json:"agent_id,omitempty"`
	AgentName           string `json:"agent_name,omitempty"`
	SessionName         string `json:"session_name,omitempty"`
	ClientKind          string `json:"client_kind,omitempty"`
	AFSVersion          string `json:"afs_version,omitempty"`
	Hostname            string `json:"hostname,omitempty"`
	OS                  string `json:"os,omitempty"`
	LocalPath           string `json:"local_path,omitempty"`
	Label               string `json:"label,omitempty"`
	User                string `json:"user,omitempty"`
	Readonly            bool   `json:"readonly,omitempty"`
	WorkspaceID         string `json:"workspace_id"`
	WorkspaceGeneration string `json:"workspace_generation,omitempty"`
	StorageProbeValue   string `json:"storage_probe_value,omitempty"`
}

type Session struct {
	SessionID                string `json:"session_id"`
	AgentID                  string `json:"agent_id,omitempty"`
	WorkspaceID              string `json:"workspace_id"`
	Readonly                 bool   `json:"readonly"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
	StorageProbeKey          string `json:"storage_probe_key,omitempty"`
	StorageProbeValue        string `json:"storage_probe_value,omitempty"`
}

type Status struct {
	Configured bool   `json:"configured"`
	Registered bool   `json:"registered"`
	SessionID  string `json:"session_id,omitempty"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

// Probe reads a short-lived server challenge through the daemon's existing
// Redis connection, ensuring management and data traffic address the same store.
type Probe func(context.Context, string) (string, error)

type Lifecycle struct {
	settings     Settings
	registration Registration
	client       *http.Client
	probe        Probe
	warn         func(string)
	interval     time.Duration
	timeout      time.Duration
	mu           sync.Mutex
	status       Status
	cancel       context.CancelFunc
	done         chan struct{}
	once         sync.Once
}

// Start attempts registration once and then retries in the background. Failure
// is reported in Status and through warn; the caller retains its file connection.
// Session/agent IDs are chosen and persisted before daemon launch and the server
// must preserve them, so recovery cannot change attribution mid-mount.
func Start(ctx context.Context, settings Settings, registration Registration, probe Probe, warn func(string)) *Lifecycle {
	if settings.URL == "" {
		return nil
	}
	return start(ctx, settings, registration, probe, warn, 20*time.Second, 3*time.Second)
}

func start(ctx context.Context, settings Settings, registration Registration, probe Probe, warn func(string), interval, timeout time.Duration) *Lifecycle {
	ctx, cancel := context.WithCancel(ctx)
	l := &Lifecycle{settings: settings, registration: registration, probe: probe, warn: warn, interval: interval, timeout: timeout,
		client: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		status: Status{Configured: true, SessionID: registration.SessionID}, cancel: cancel, done: make(chan struct{})}
	l.update(l.register(ctx))
	go l.run(ctx)
	return l
}

func (l *Lifecycle) Snapshot() *Status {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	status := l.status
	return &status
}

func (l *Lifecycle) update(err error) {
	l.mu.Lock()
	previous := l.status.LastError
	l.status.Registered = err == nil
	if err == nil {
		l.status.LastError = ""
		l.status.LastSeenAt = time.Now().UTC().Format(time.RFC3339Nano)
	} else {
		l.status.LastError = err.Error()
	}
	current := l.status.LastError
	l.mu.Unlock()
	if current != "" && current != previous && l.warn != nil {
		l.warn(current)
	}
}

func (l *Lifecycle) run(ctx context.Context) {
	defer close(l.done)
	timer := time.NewTimer(l.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			var err error
			if l.Snapshot().Registered {
				err = l.request(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(l.registration.SessionID)+"/heartbeat", l.registration, nil)
				if errors.Is(err, errMissing) {
					err = l.register(ctx)
				}
			} else {
				err = l.register(ctx)
			}
			l.update(err)
			timer.Reset(l.interval)
		}
	}
}

func (l *Lifecycle) register(ctx context.Context) error {
	if err := l.settings.Validate(); err != nil {
		return err
	}
	if l.registration.SessionID == "" || l.registration.WorkspaceID == "" {
		return errors.New("control plane registration requires a persisted session and workspace identity")
	}
	var response Session
	if err := l.request(ctx, http.MethodPost, "/v1/workspaces/"+url.PathEscape(l.registration.WorkspaceID)+"/sessions", l.registration, &response); err != nil {
		return err
	}
	if response.SessionID != l.registration.SessionID || response.WorkspaceID != l.registration.WorkspaceID || (response.AgentID != "" && response.AgentID != l.registration.AgentID) {
		return errors.New("control plane returned a different session or workspace identity")
	}
	if l.probe != nil {
		if response.StorageProbeKey != "afs:management:probe:"+l.registration.SessionID || response.StorageProbeValue == "" {
			return errors.New("control plane did not provide Redis storage verification")
		}
		probeCtx, cancel := context.WithTimeout(ctx, l.timeout)
		value, err := l.probe(probeCtx, response.StorageProbeKey)
		cancel()
		if err != nil || value != response.StorageProbeValue {
			_ = l.request(ctx, http.MethodDelete, "/v1/sessions/"+url.PathEscape(l.registration.SessionID), nil, nil)
			return errors.New("control plane and mount do not share the same Redis workspace")
		}
	}
	confirmation := l.registration
	confirmation.StorageProbeValue = response.StorageProbeValue
	if err := l.request(ctx, http.MethodPost, "/v1/sessions/"+url.PathEscape(l.registration.SessionID)+"/heartbeat", confirmation, nil); err != nil {
		return err
	}
	if response.HeartbeatIntervalSeconds > 0 && response.HeartbeatIntervalSeconds <= 60 {
		l.interval = time.Duration(response.HeartbeatIntervalSeconds) * time.Second
	}
	return nil
}

var errMissing = errors.New("control plane session is missing")

func (l *Lifecycle) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return errors.New("cannot encode control plane request")
		}
		body = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(l.settings.URL, "/")+path, body)
	if err != nil {
		return errors.New("invalid control plane endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	if l.settings.Token != "" {
		req.Header.Set("Authorization", "Bearer "+l.settings.Token)
	}
	response, err := l.client.Do(req)
	if err != nil {
		return errors.New("control plane unavailable; direct Redis access continues")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return errMissing
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("control plane request failed (HTTP %d)", response.StatusCode)
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(output); err != nil {
			return errors.New("invalid control plane response")
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	}
	return nil
}

func (l *Lifecycle) Close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.cancel()
		<-l.done
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
		defer cancel()
		if err := l.request(ctx, http.MethodDelete, "/v1/sessions/"+url.PathEscape(l.registration.SessionID), nil, nil); err != nil && !errors.Is(err, errMissing) && l.warn != nil {
			l.warn(err.Error())
		}
	})
}
