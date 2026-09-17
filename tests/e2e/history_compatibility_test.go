//go:build integration

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
)

func TestHistoryCompatibilityCLIAndHTTPProcess(t *testing.T) {
	r := newRedis(t)
	c := newCLI(t, r)
	c.run(nil, "create", "compatible")
	c.run(nil, "history", "policy", "compatible", "--mode", "all")
	c.put("compatible", "file.txt", []byte("old\n"))
	c.run(nil, "cp", "create", "compatible", "--name", "old")
	c.put("compatible", "file.txt", []byte("new\n"))
	c.closeWriter("compatible")
	var first controlplane.FileHistoryResponse
	if err := json.Unmarshal(c.run(nil, "--json", "history", "list", "compatible", "file.txt", "--order", "asc", "--limit", "1"), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Lineages) != 1 || first.NextCursor == "" {
		t.Fatalf("history page: %+v", first)
	}
	version := first.Lineages[0].Versions[0]
	var second controlplane.FileHistoryResponse
	if err := json.Unmarshal(c.run(nil, "--json", "history", "list", "compatible", "file.txt", "--order", "asc", "--limit", "1", "--cursor", first.NextCursor), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Lineages) != 1 || second.Lineages[0].Versions[0].Ordinal <= version.Ordinal {
		t.Fatalf("second history page: %+v", second)
	}
	if shown := c.run(nil, "--json", "history", "show", "compatible", "file.txt", "--version", version.VersionID); !bytes.Contains(shown, []byte(`"content":"old\n"`)) {
		t.Fatalf("show: %s", shown)
	}
	diff := c.run(nil, "--json", "history", "diff", "compatible", "file.txt", "--from-version", version.VersionID, "--to-ref", "working-copy")
	if !bytes.Contains(diff, []byte("-old")) || !bytes.Contains(diff, []byte("+new")) {
		t.Fatalf("CLI diff: %s", diff)
	}
	c.run(nil, "history", "restore", "compatible", "file.txt", "--version", version.VersionID)
	if got := c.published("compatible", "file.txt"); string(got) != "old\n" {
		t.Fatalf("CLI restore: %q", got)
	}
	c.removePublished("compatible", "file.txt")
	c.closeWriter("compatible")
	c.run(nil, "history", "undelete", "compatible", "file.txt")
	if got := c.published("compatible", "file.txt"); string(got) != "old\n" {
		t.Fatalf("CLI undelete: %q", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	process := c.command(ctx, "--json", "history", "serve", "--listen", "127.0.0.1:0", "--database-id", "selected", "--allow-origin", "http://localhost:5173")
	output, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "serve.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	process.Stderr = log
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = process.Process.Signal(syscall.SIGTERM)
			_ = process.Wait()
		}
	}()
	type announcement struct {
		URL        string `json:"url"`
		DatabaseID string `json:"database_id"`
	}
	started := make(chan announcement, 1)
	startError := make(chan error, 1)
	go func() {
		var message announcement
		err := json.NewDecoder(output).Decode(&message)
		if err != nil {
			startError <- err
			return
		}
		started <- message
	}()
	var address string
	select {
	case message := <-started:
		address = message.URL
	case err := <-startError:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("history server did not announce its listener")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	request := func(method, path string, body any, origin string) (int, []byte, http.Header) {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, err := http.NewRequest(method, address+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, payload, response.Header
	}
	base := "/v1/databases/selected/workspaces/compatible"
	status, payload, headers := request(http.MethodGet, base+"/files/history?path=/file.txt&limit=50", nil, "http://localhost:5173")
	if status != 200 || headers.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("HTTP history %d %s", status, payload)
	}
	var history controlplane.FileHistoryResponse
	if err := json.Unmarshal(payload, &history); err != nil || len(history.Lineages) == 0 {
		t.Fatalf("HTTP history %s %v", payload, err)
	}
	status, payload, _ = request(http.MethodPost, base+"/files/diff", map[string]any{"path": "/file.txt", "from": map[string]any{"version_id": version.VersionID}, "to": map[string]any{"ref": "working-copy"}}, "http://localhost:5173")
	if status != 200 {
		t.Fatalf("HTTP diff %d %s", status, payload)
	}
	status, payload, _ = request(http.MethodPost, base+":restore-version", map[string]any{"path": "/file.txt", "version_id": version.VersionID}, "https://untrusted.example")
	if status != 403 {
		t.Fatalf("HTTP origin policy %d %s", status, payload)
	}
	status, payload, _ = request(http.MethodPost, base+":restore-version", map[string]any{"path": "/file.txt", "version_id": version.VersionID}, "http://localhost:5173")
	if status != 200 || !strings.Contains(string(payload), "restored_from_version_id") {
		t.Fatalf("HTTP restore %d %s", status, payload)
	}
	status, payload, _ = request(http.MethodGet, base+"/files/version-content?file_id="+version.FileID+"&ordinal=1", nil, "http://localhost:5173")
	if status != 200 || !strings.Contains(string(payload), `"content":"old\n"`) {
		t.Fatalf("HTTP content %d %s", status, payload)
	}
	listed := readHistoryCLI(t, c, "compatible", "file.txt")
	if len(listed.Lineages) != 1 || listed.Lineages[0].FileID != version.FileID {
		t.Fatalf("CLI listing lost restored lineage: %+v", listed)
	}
	c.run(nil, "history", "policy", "compatible", "--mode", "off")
	c.put("compatible", "off.txt", []byte("activity without snapshots"))
	c.closeWriter("compatible")
	status, payload, _ = request(http.MethodGet, base+"/changes?path=/off.txt&direction=desc", nil, "http://localhost:5173")
	var activity controlplane.FileHistoryChangesResponse
	if err := json.Unmarshal(payload, &activity); status != 200 || err != nil || len(activity.Entries) == 0 {
		t.Fatalf("HTTP activity with capture disabled %d %s %v", status, payload, err)
	}
	foundPut := false
	for _, entry := range activity.Entries {
		if entry.VersionID != "" || entry.Origin == "" || entry.Op == "inode" || entry.Op == "dir" {
			t.Fatalf("disabled activity included version or cache notification: %+v", entry)
		}
		foundPut = foundPut || entry.Op == "put"
	}
	if !foundPut {
		t.Fatalf("HTTP activity omitted file write: %+v", activity)
	}
	if err := process.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("history server shutdown: %v", err)
	}
	waited = true
}
