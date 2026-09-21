//go:build integration

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var controlPlaneBuild struct {
	sync.Once
	path string
	err  error
}

func controlPlaneBinary(t *testing.T) string {
	t.Helper()
	controlPlaneBuild.Do(func() {
		dir, err := os.MkdirTemp("", "afs-control-plane-e2e-")
		if err != nil {
			controlPlaneBuild.err = err
			return
		}
		controlPlaneBuild.path = filepath.Join(dir, "afs-control-plane")
		_, file, _, _ := runtime.Caller(0)
		build := exec.Command("go", "build", "-o", controlPlaneBuild.path, "./cmd/afs-control-plane")
		build.Dir = filepath.Dir(filepath.Dir(filepath.Dir(file)))
		out, err := build.CombinedOutput()
		if err != nil {
			controlPlaneBuild.err = fmt.Errorf("build control plane: %w\n%s", err, out)
		}
	})
	if controlPlaneBuild.err != nil {
		t.Fatal(controlPlaneBuild.err)
	}
	return controlPlaneBuild.path
}

type managementProcess struct {
	t                    *testing.T
	redis                *redisServer
	addr, token, logPath string
	redisURL             string
	databasesFile        string
	cmd                  *exec.Cmd
}

func newManagementProcess(t *testing.T, r *redisServer, redisURL ...string) *managementProcess {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := &managementProcess{t: t, redis: r, addr: listener.Addr().String(), token: "isolated-management-test-token", logPath: filepath.Join(dir, "server.log"), databasesFile: filepath.Join(dir, "databases.json")}
	if len(redisURL) != 0 {
		p.redisURL = redisURL[0]
	}
	_ = listener.Close()
	p.start()
	t.Cleanup(func() {
		p.stop()
		if t.Failed() {
			raw, _ := os.ReadFile(p.logPath)
			t.Logf("control-plane log: %s", raw)
		}
	})
	return p
}
func (p *managementProcess) start() {
	p.t.Helper()
	endpoint := p.redisURL
	if endpoint == "" {
		endpoint = p.redis.url()
	}
	p.cmd = exec.Command(controlPlaneBinary(p.t), "--listen", p.addr, "--redis", endpoint, "--databases-file", p.databasesFile)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "AFS_") {
			p.cmd.Env = append(p.cmd.Env, entry)
		}
	}
	p.cmd.Env = append(p.cmd.Env, "AFS_CONTROL_PLANE_TOKEN="+p.token)
	log, err := os.OpenFile(p.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		p.t.Fatal(err)
	}
	p.cmd.Stdout = log
	p.cmd.Stderr = log
	err = p.cmd.Start()
	_ = log.Close()
	if err != nil {
		p.t.Fatal(err)
	}
	eventually(p.t, 10*time.Second, "management process readiness", func() bool {
		request, _ := http.NewRequest(http.MethodGet, "http://"+p.addr+"/v1/auth/config", nil)
		response, err := (&http.Client{Timeout: time.Second}).Do(request)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == 200
	})
}
func (p *managementProcess) stop() {
	if p.cmd == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
	p.cmd = nil
}
func (p *managementProcess) request(method, path string, body any) []byte {
	p.t.Helper()
	status, raw := p.requestStatus(method, path, body)
	if status < 200 || status >= 300 {
		p.t.Fatalf("%s %s: status %d: %s", method, path, status, raw)
	}
	return raw
}
func (p *managementProcess) requestStatus(method, path string, body any) (int, []byte) {
	p.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			p.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, "http://"+p.addr+path, reader)
	if err != nil {
		p.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		p.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		p.t.Fatal(err)
	}
	return response.StatusCode, raw
}

type observedManagedSession struct {
	ID          string `json:"session_id"`
	WorkspaceID string `json:"workspace_id"`
	DatabaseID  string `json:"database_id"`
	State       string `json:"state"`
	LastSeen    string `json:"last_seen_at"`
	LocalPath   string `json:"local_path"`
	Hostname    string `json:"hostname"`
}

func (p *managementProcess) sessions() []observedManagedSession {
	p.t.Helper()
	var result struct {
		Items []observedManagedSession `json:"items"`
	}
	if err := json.Unmarshal(p.request("GET", "/v1/agents", nil), &result); err != nil {
		p.t.Fatal(err)
	}
	return result.Items
}

// Real processes prove management integration while preserving the direct data
// path: Redis remains available when the separate HTTP server is stopped.
func TestControlPlaneManagedSyncLifecycle(t *testing.T) {
	r := newRedis(t)
	p := newManagementProcess(t, r)
	t.Setenv("AFS_CONTROL_PLANE_URL", "http://"+p.addr)
	t.Setenv("AFS_CONTROL_PLANE_TOKEN", p.token)
	c := newCLI(t, r)
	c.managed = true
	const workspace = "managed-shared"
	c.run(nil, "create", workspace)
	root := filepath.Join(t.TempDir(), "mount")
	c.mount(workspace, root)
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	var session observedManagedSession
	eventually(t, 15*time.Second, "registered sync session", func() bool {
		for _, item := range p.sessions() {
			if item.LocalPath == physicalRoot && item.State == "active" {
				session = item
				return item.ID != "" && item.Hostname != ""
			}
		}
		return false
	})
	endpoint := "/v1/workspaces/" + session.WorkspaceID
	// The actual auth boundary protects management reads and mutations.
	response, err := http.Get("http://" + p.addr + "/v1/agents")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated agents returned %d", response.StatusCode)
	}

	streamRequest, _ := http.NewRequest("GET", "http://"+p.addr+"/v1/monitor/stream", nil)
	streamRequest.Header.Set("Authorization", "Bearer "+p.token)
	stream, err := (&http.Client{Timeout: 15 * time.Second}).Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatalf("monitor stream: %d", stream.StatusCode)
	}
	eventsSeen := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stream.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "event: ") {
				select {
				case eventsSeen <- strings.TrimPrefix(scanner.Text(), "event: "):
				default:
				}
			}
		}
	}()
	select {
	case event := <-eventsSeen:
		if event != "ready" {
			t.Fatalf("first monitor event: %s", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not become ready")
	}
	first := []byte("published with a managed session\n")
	write(t, filepath.Join(root, "file.txt"), first)
	awaitRemote(t, c, workspace, "file.txt", first)
	select {
	case event := <-eventsSeen:
		if event != "monitor" {
			t.Fatalf("change monitor event: %s", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("direct daemon write did not notify browser monitor")
	}
	_ = stream.Body.Close()

	raw := p.request("GET", endpoint+"/changes", nil)
	if !bytes.Contains(raw, []byte(session.ID)) {
		t.Fatalf("published activity lacks managed session: %s", raw)
	}
	p.request("POST", endpoint+":save-from-live", map[string]any{"checkpoint_id": "browser-save", "allow_unchanged": true})
	tree := p.request("GET", endpoint+"/tree?view=checkpoint:browser-save&path=/", nil)
	if !bytes.Contains(tree, []byte("file.txt")) {
		t.Fatalf("checkpoint browser lost file: %s", tree)
	}

	// Heartbeats advance even with no writes; presence is not inferred from files.
	eventually(t, 45*time.Second, "idle management heartbeat", func() bool {
		for _, item := range p.sessions() {
			if item.ID == session.ID && item.LastSeen != session.LastSeen {
				return true
			}
		}
		return false
	})
	p.stop()
	second := []byte("sync continues with the management server offline\n")
	write(t, filepath.Join(root, "file.txt"), second)
	awaitRemote(t, c, workspace, "file.txt", second)
	c.run(nil, "sync", "--wait", root, "--timeout", "20s")
	p.start()
	eventually(t, 45*time.Second, "session survives management restart", func() bool {
		for _, item := range p.sessions() {
			if item.ID == session.ID && item.State == "active" {
				return true
			}
		}
		return false
	})
	third := []byte("same identity after sync worker restart\n")
	write(t, filepath.Join(root, "file.txt"), third)
	awaitRemote(t, c, workspace, "file.txt", third)
	raw = p.request("GET", endpoint+"/changes?path=/file.txt", nil)
	if !bytes.Contains(raw, []byte(session.ID)) {
		t.Fatalf("restarted sync lost attribution: %s", raw)
	}
	c.unmount(root)
	eventually(t, 10*time.Second, "managed session closed after unmount", func() bool {
		for _, item := range p.sessions() {
			if item.ID == session.ID && item.State == "closed" {
				return true
			}
		}
		return false
	})
	events := p.request("GET", endpoint+"/events", nil)
	if !bytes.Contains(events, []byte(session.ID)) || !bytes.Contains(events, []byte("checkpoint")) {
		t.Fatalf("combined lifecycle history incomplete: %s", events)
	}
	// CLI creations are immediately visible through the browser API.
	list := p.request("GET", "/v1/workspaces", nil)
	if !bytes.Contains(list, []byte(workspace)) {
		t.Fatalf("CLI workspace missing from browser: %s", list)
	}
}
