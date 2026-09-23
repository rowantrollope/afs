//go:build integration

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
)

func TestBrowserAuthCLIApprovalExchangeAndRevocation(t *testing.T) {
	r := newRedis(t)
	p := newManagementProcess(t, r)
	c := newManagedCLI(t, r, "http://"+p.addr, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := c.command(ctx, "--json", "auth", "login", "--no-browser", "--name", "Browser process test")
	var output bytes.Buffer
	command.Stdout = &output
	diagnostics, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	scanner := bufio.NewScanner(diagnostics)
	var messages strings.Builder
	var requestID, userCode string
	for scanner.Scan() {
		line := scanner.Text()
		messages.WriteString(line + "\n")
		if strings.Contains(line, "/connect-cli?request=") {
			u, err := url.Parse(strings.TrimSpace(line))
			if err != nil || u.Host != p.addr {
				t.Fatal("approval URL left selected server")
			}
			requestID = u.Query().Get("request")
		}
		if strings.HasPrefix(line, "Confirm this code matches: ") {
			userCode = strings.TrimPrefix(line, "Confirm this code matches: ")
			break
		}
	}
	if requestID == "" || userCode == "" {
		t.Fatal("CLI did not print approval instructions")
	}
	var request struct {
		Name   string `json:"name"`
		Code   string `json:"user_code"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(p.request("GET", "/v1/auth/cli/requests/"+requestID, nil), &request); err != nil {
		t.Fatal(err)
	}
	if request.Name != "Browser process test" || request.Code != userCode || request.Status != "pending" {
		t.Fatal("approval screen does not match CLI")
	}
	p.request("POST", "/v1/auth/cli/requests/"+requestID, map[string]string{"decision": "approve", "user_code": userCode})
	for scanner.Scan() {
		messages.WriteString(scanner.Text() + "\n")
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("CLI login: %v, %s", err, messages.String())
	}
	_, config := readAuthConfig(t, c.config)
	settings := config["controlPlane"].(map[string]any)
	token, _ := settings["token"].(string)
	if token == "" || token == p.token || !strings.HasPrefix(token, "afs_key_") {
		t.Fatal("CLI did not save its own named API key")
	}
	assertAuthConfigPrivate(t, c.config)
	assertAuthOutputHasNoSecrets(t, append(output.Bytes(), []byte(messages.String())...), token, p.token)
	var listed controlplane.APIKeyList
	if err := json.Unmarshal(p.request("GET", "/v1/api-keys", nil), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Keys) != 1 || listed.Keys[0].Name != "Browser process test" || listed.Keys[0].ExpiresAt == "" {
		t.Fatal("named expiring key not registered")
	}
	// CLI config survives a server restart and permits normal managed actions.
	p.stop()
	p.start()
	c.run(nil, "create", "browser-login-workspace")
	c.run(nil, "list")
	before, _ := os.ReadFile(c.config)
	authResult(t, c, nil, []string{token, p.token}, "login")
	after, _ := os.ReadFile(c.config)
	if !bytes.Equal(before, after) {
		t.Fatal("repeat login changed saved key")
	}
	p.request("DELETE", "/v1/api-keys/"+listed.Keys[0].ID, nil)
	c.mustFail("list")
}
