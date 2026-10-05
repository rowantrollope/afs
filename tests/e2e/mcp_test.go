//go:build integration

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rowantrollope/afs/internal/mcpfiles"
)

func TestMCPIndependentClientsAndRecovery(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(source)))
	mcpBinary := filepath.Join(t.TempDir(), "afs-mcp")
	build := exec.Command("go", "build", "-o", mcpBinary, "./cmd/afs-mcp")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build MCP: %v %s", err, out)
	}
	for _, transport := range []string{"stdio", "http"} {
		t.Run(transport, func(t *testing.T) {
			r := newRedis(t)
			c := newCLI(t, r)
			c.environment = map[string]string{}
			seed := t.TempDir()
			write(t, filepath.Join(seed, "readme.md"), []byte("# shared folder\nHello cloud agents.\n"))
			c.run(nil, "create", "agentconnect", "--from", seed)
			mount := filepath.Join(t.TempDir(), "folder")
			pid := c.mount("agentconnect", mount)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			args := []string{"--workspace", "agentconnect", "--directory", mount, "--afs-bin", binary, "--config", c.config, "--state-dir", c.state, "--timeout", "10s", "--transport", transport}
			command := func(extra ...string) *exec.Cmd {
				cmd := exec.Command(mcpBinary, append(args, extra...)...)
				cmd.Env = c.command(ctx).Env
				cmd.Stderr = os.Stderr
				return cmd
			}
			var endpoint, token string
			var httpCmd *exec.Cmd
			if transport == "http" {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				address := listener.Addr().String()
				listener.Close()
				endpoint = "http://" + address + "/mcp"
				token = fmt.Sprintf("%x", sha256.Sum256([]byte(t.TempDir())))
				httpCmd = command("--listen", address)
				httpCmd.Env = append(httpCmd.Env, "AFS_MCP_TOKEN="+token)
				logPath := filepath.Join(t.TempDir(), "mcp.stderr")
				log, err := os.Create(logPath)
				if err != nil {
					t.Fatal(err)
				}
				defer log.Close()
				httpCmd.Stderr = log
				if err = httpCmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if httpCmd != nil {
						httpCmd.Process.Signal(syscall.SIGTERM)
						httpCmd.Wait()
					}
				}()
				eventually(t, 20*time.Second, "MCP readiness after AFS verification", func() bool {
					data, _ := os.ReadFile(logPath)
					return strings.Contains(string(data), "afs-mcp ready at "+endpoint)
				})
				response, err := http.Post(endpoint, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusUnauthorized {
					t.Fatalf("anonymous access: %d", response.StatusCode)
				}
			}
			connect := func(name string) *mcp.ClientSession {
				client := mcp.NewClient(&mcp.Implementation{Name: name, Version: "1"}, nil)
				var tr mcp.Transport
				if transport == "stdio" {
					tr = &mcp.CommandTransport{Command: command()}
				} else {
					tr = &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: mcpBearerTransport{token: token}}}
				}
				cs, err := client.Connect(ctx, tr, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cs.Close() })
				if cs.InitializeResult().ServerInfo.Name != "afs-mcp" {
					t.Fatal("initialization failed")
				}
				tools, err := cs.ListTools(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				names := map[string]bool{}
				for _, tool := range tools.Tools {
					names[tool.Name] = true
				}
				if len(names) != 4 || !names["list_files"] || !names["read_file"] || !names["write_file"] || !names["workspace_status"] {
					t.Fatalf("discovery: %+v", names)
				}
				return cs
			}
			a, b := connect("agent-A"), connect("agent-B")
			call := func(client *mcp.ClientSession, name string, in any, wantError bool) mcpfiles.Result {
				t.Helper()
				res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: in})
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if res.IsError != wantError {
					t.Fatalf("%s error=%v: %+v", name, res.IsError, res)
				}
				data, err := json.Marshal(res.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				var out mcpfiles.Result
				if err = json.Unmarshal(data, &out); err != nil {
					t.Fatal(err)
				}
				if wantError && out.Error == nil {
					t.Fatalf("missing structured error: %s", data)
				}
				return out
			}
			for _, client := range []*mcp.ClientSession{a, b} {
				status := call(client, "workspace_status", map[string]any{}, false)
				if !status.Ready || status.Workspace != "agentconnect" {
					t.Fatalf("status: %+v", status)
				}
				listed := call(client, "list_files", map[string]any{}, false)
				if listed.Files == nil || len(*listed.Files) != 1 || (*listed.Files)[0].Path != "readme.md" {
					t.Fatalf("list: %+v", listed)
				}
				read := call(client, "read_file", mcpfiles.FileInput{Path: "readme.md"}, false)
				if read.Content == nil || !strings.Contains(*read.Content, "Hello cloud agents.") {
					t.Fatalf("readme: %+v", read)
				}
			}
			input := func(path, content string) mcpfiles.WriteInput {
				return mcpfiles.WriteInput{Path: path, Content: content, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(content)))}
			}
			first := input("reports/from-a.md", "Agent A: complete\n")
			out := call(a, "write_file", first, false)
			if out.Created == nil || !*out.Created || !out.Verified {
				t.Fatalf("write receipt: %+v", out)
			}
			read := call(b, "read_file", mcpfiles.FileInput{Path: first.Path}, false)
			if read.Content == nil || *read.Content != first.Content || read.SHA256 != first.SHA256 {
				t.Fatalf("cross-client read: %+v", read)
			}
			second := input("reports/from-b.md", "Agent B: received\n")
			call(b, "write_file", second, false)
			read = call(a, "read_file", mcpfiles.FileInput{Path: second.Path}, false)
			if read.SHA256 != second.SHA256 {
				t.Fatal("round trip failed")
			}
			out = call(b, "write_file", first, false)
			if out.Created == nil || *out.Created || !out.Verified {
				t.Fatalf("identical retry: %+v", out)
			}
			out = call(b, "write_file", input(first.Path, "overwrite"), true)
			if out.Error.Code != "already_exists" {
				t.Fatalf("conflict: %+v", out)
			}
			// Two simultaneous clients must not publish mixed or partial bytes.
			var wg sync.WaitGroup
			results := make(chan *mcp.CallToolResult, 2)
			errs := make(chan error, 2)
			for i, client := range []*mcp.ClientSession{a, b} {
				wg.Add(1)
				go func(i int, client *mcp.ClientSession) {
					defer wg.Done()
					res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "write_file", Arguments: input("claim", fmt.Sprintf("writer-%d", i))})
					results <- res
					errs <- err
				}(i, client)
			}
			wg.Wait()
			successes := 0
			for range 2 {
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
				if res := <-results; !res.IsError {
					successes++
				}
			}
			if successes != 1 {
				t.Fatalf("exclusive create winners: %d", successes)
			}
			outside := t.TempDir()
			write(t, filepath.Join(outside, "secret"), []byte("not shared"))
			if err := os.Symlink(outside, filepath.Join(mount, "escape")); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"../secret", "/readme.md", ".afs-lite-sync/requests", "escape/secret"} {
				call(a, "read_file", mcpfiles.FileInput{Path: p}, true)
				call(b, "write_file", input(p, "forbidden"), true)
			}
			// Redis outage must turn readiness false even though local files exist.
			r.stop()
			call(a, "workspace_status", map[string]any{}, true)
			unavailable := command("--transport", "stdio")
			if err := unavailable.Run(); err == nil {
				t.Fatal("adapter initialized with unavailable Redis")
			}
			r.start()
			// Terminate adapter clients and the owned mount, then recover Redis and
			// AFS from retained state. Never signal a PID from an external registry.
			a.Close()
			b.Close()
			if httpCmd != nil {
				httpCmd.Process.Signal(syscall.SIGTERM)
				httpCmd.Wait()
				httpCmd = nil
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			eventually(t, 10*time.Second, "daemon lock released", func() bool {
				out, _, err := c.runTimeout(5*time.Second, nil, "--json", "sync", "status", mount)
				return err == nil && strings.Contains(string(out), `"state":"stopped"`)
			})
			r.stop()
			r.start()
			c.mount("agentconnect", mount)
			wrongWorkspace := command("--transport", "stdio", "--workspace", "another-workspace")
			if err := wrongWorkspace.Run(); err == nil {
				t.Fatal("adapter initialized against wrong workspace")
			}
			if transport == "http" {
				httpCmd = command("--listen", strings.TrimSuffix(strings.TrimPrefix(endpoint, "http://"), "/mcp"))
				httpCmd.Env = append(httpCmd.Env, "AFS_MCP_TOKEN="+token)
				if err := httpCmd.Start(); err != nil {
					t.Fatal(err)
				}
				eventually(t, 20*time.Second, "restarted HTTP adapter readiness", func() bool {
					response, err := http.Get(endpoint)
					if err != nil {
						return false
					}
					response.Body.Close()
					return response.StatusCode == http.StatusUnauthorized
				})
			}
			recovered := connect("agent-after-restart")
			read = call(recovered, "read_file", mcpfiles.FileInput{Path: first.Path}, false)
			if read.SHA256 != first.SHA256 {
				t.Fatalf("restart recovery: %+v", read)
			}
			call(recovered, "write_file", first, false)
			fresh := newCLI(t, r)
			fresh.environment = map[string]string{}
			freshRoot := filepath.Join(t.TempDir(), "fresh")
			fresh.mount("agentconnect", freshRoot)
			if data, err := os.ReadFile(filepath.Join(freshRoot, second.Path)); err != nil || string(data) != second.Content {
				t.Fatalf("new mount Redis recovery: %q %v", data, err)
			}
			t.Log("PASS: initialize/discovery, list/read readme.md, two independent clients write/read, exclusive races, retry/conflict, path/token isolation, Redis and daemon restart, fresh mount hydration")
		})
	}
}

type mcpBearerTransport struct{ token string }

func (b mcpBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
