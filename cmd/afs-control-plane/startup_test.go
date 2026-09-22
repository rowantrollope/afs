package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
)

// Run the actual command entrypoint in an isolated process so these checks
// exercise startup, listener readiness, signal handling and SQLite shutdown.
func TestControlPlaneHelperProcess(t *testing.T) {
	if os.Getenv("AFS_TEST_CONTROL_PLANE_PROCESS") != "1" {
		return
	}
	for i, argument := range os.Args {
		if argument == "--" {
			os.Args = append([]string{"afs-control-plane"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing command arguments")
}

func controlPlaneProcess(args ...string) *exec.Cmd {
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestControlPlaneHelperProcess$", "--"}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "AFS_") && !strings.HasPrefix(value, "VERCEL=") && !strings.HasPrefix(value, "PORT=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "AFS_TEST_CONTROL_PLANE_PROCESS=1")
	return command
}

func TestHostedStartupFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name        string
		token       string
		metadataURL string
		port        string
		want        string
	}{
		{"missing-token", "", "postgres://user:private-password@metadata.invalid/afs", "3000", "AFS_CONTROL_PLANE_TOKEN is required"},
		{"missing-postgres", "test-token", "", "3000", "AFS_METADATA_URL must specify PostgreSQL"},
		{"invalid-postgres", "test-token", "postgres://user:private-password@[invalid", "3000", "AFS_METADATA_URL must be a valid PostgreSQL URL"},
		{"sqlite-url", "test-token", "sqlite:///private-password.sqlite", "3000", "AFS_METADATA_URL must be a valid PostgreSQL URL"},
		{"invalid-port", "test-token", "postgres://user:private-password@metadata.invalid/afs", "private-invalid-port", "PORT must be an integer"},
		{"driver-error", "test-token", "postgres://user:private-password@metadata.invalid/afs?sslmode=private-driver-error", "3000", "cannot open PostgreSQL metadata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "must-not-create.sqlite")
			command := controlPlaneProcess("--hosted=false", "--listen", "127.0.0.1:0", "--metadata-file", path)
			command.Env = append(command.Env, "VERCEL=1", "PORT="+test.port, "AFS_CONTROL_PLANE_TOKEN="+test.token, "AFS_METADATA_URL="+test.metadataURL)
			output, err := command.CombinedOutput()
			if err == nil || !bytes.Contains(output, []byte(test.want)) {
				t.Fatalf("unexpected hosted startup result: %v\n%s", err, output)
			}
			for _, secret := range []string{"private-password", "private-invalid-port", "private-driver-error"} {
				if bytes.Contains(output, []byte(secret)) {
					t.Fatal("hosted startup failure disclosed private configuration")
				}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("hosted startup must never initialize fallback SQLite")
			}
		})
	}
}

func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestControlPlaneStartsWithoutAvailableRedis(t *testing.T) {
	for _, name := range []string{"empty", "offline-initial-database", "initialized-invalid-seed"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			address := unusedLoopbackAddress(t)
			metadataPath := filepath.Join(directory, "metadata.sqlite")
			args := []string{"--listen", address, "--metadata-file", metadataPath, "--databases-file", filepath.Join(directory, "legacy.json")}
			withOfflineSeed := name == "offline-initial-database"
			if withOfflineSeed {
				args = append(args, "--redis", "redis://"+unusedLoopbackAddress(t)+"/0")
			}
			if name == "initialized-invalid-seed" {
				metadata, err := controlplane.OpenMetadataStore(metadataPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := metadata.SaveProfiles(context.Background(), nil, ""); err != nil {
					_ = metadata.Close()
					t.Fatal(err)
				}
				if err := metadata.Close(); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--redis", "invalid-unused-seed-url")
			}
			command := controlPlaneProcess(args...)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var processErr error
			go func() {
				processErr = command.Wait()
				close(done)
			}()
			t.Cleanup(func() {
				_ = command.Process.Signal(os.Interrupt)
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					_ = command.Process.Kill()
					<-done
				}
				if processErr != nil {
					t.Errorf("control plane exited: %v\n%s", processErr, output.String())
				}
			})
			client := &http.Client{Timeout: time.Second}
			deadline := time.Now().Add(10 * time.Second)
			for {
				select {
				case <-done:
					t.Fatalf("control plane stopped before readiness: %v\n%s", processErr, output.String())
				default:
				}
				response, err := client.Get("http://" + address + "/healthz")
				if err == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if response.StatusCode == http.StatusOK {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("metadata readiness did not become healthy without Redis")
				}
				time.Sleep(20 * time.Millisecond)
			}
			client.Timeout = 10 * time.Second
			response, err := client.Get("http://" + address + "/v1/databases")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var databases struct {
				Items []json.RawMessage `json:"items"`
			}
			if err := json.NewDecoder(response.Body).Decode(&databases); err != nil {
				t.Fatal(err)
			}
			want := 0
			if withOfflineSeed {
				want = 1
			}
			if response.StatusCode != http.StatusOK || len(databases.Items) != want {
				t.Fatalf("database listing: HTTP %d, got %d profiles, want %d", response.StatusCode, len(databases.Items), want)
			}
		})
	}
}

func TestControlPlaneRejectsUnauthenticatedRemoteStartup(t *testing.T) {
	metadata := filepath.Join(t.TempDir(), "metadata.sqlite")
	output, err := controlPlaneProcess("--listen", "0.0.0.0:0", "--metadata-file", metadata).CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("AFS_CONTROL_PLANE_TOKEN is required")) {
		t.Fatalf("unexpected remote startup result: %v\n%s", err, output)
	}
	if _, err := os.Stat(metadata); !os.IsNotExist(err) {
		t.Fatal("rejected startup must not initialize metadata")
	}
}

func TestControlPlaneMigrationURLFailureIsRedacted(t *testing.T) {
	directory := t.TempDir()
	output, err := controlPlaneProcess("--metadata-file", filepath.Join(directory, "metadata.sqlite"), "--databases-file", filepath.Join(directory, "legacy.json"), "--migrate-api-keys-from", "redis://user:private-migration-password@[invalid").CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte("invalid legacy API-key Redis URL")) {
		t.Fatalf("unexpected migration startup result: %v\n%s", err, output)
	}
	if bytes.Contains(output, []byte("private-migration-password")) {
		t.Fatal("migration error disclosed credentials")
	}
}
