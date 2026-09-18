package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestHTTPIdleConnStopsStalledIO(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			conn := &idleDeadlineConn{Conn: local, idle: 100 * time.Millisecond}
			done := make(chan error, 1)
			go func() {
				var err error
				if operation == "read" {
					_, err = conn.Read(make([]byte, 1))
				} else {
					_, err = conn.Write([]byte("x"))
				}
				done <- err
			}()
			select {
			case err := <-done:
				if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
					t.Fatalf("stalled %s did not time out: %v", operation, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("stalled %s was not bounded", operation)
			}
		})
	}
}

// HTTP reads a response concurrently with uploading the request. Writes must
// extend an already blocked read deadline while the upload is making progress.
func TestHTTPIdleConnAllowsActiveTransferBeyondIdleInterval(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	conn := &idleDeadlineConn{Conn: local, idle: 200 * time.Millisecond}
	response := make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 1)); response <- err }()
	server := make(chan error, 1)
	go func() {
		for i := 0; i < 8; i++ {
			if _, err := io.ReadFull(peer, make([]byte, 1)); err != nil {
				server <- err
				return
			}
		}
		_, err := peer.Write([]byte("r"))
		server <- err
	}()
	for i := 0; i < 8; i++ {
		time.Sleep(50 * time.Millisecond)
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-response; err != nil {
		t.Fatalf("active upload timed out while awaiting response: %v", err)
	}
	if err := <-server; err != nil {
		t.Fatal(err)
	}
}

func shortIdleClient(t *testing.T, endpoint string, interval time.Duration) *CLIClient {
	t.Helper()
	c, err := NewCLIClient(endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	transport := c.http.Transport.(*http.Transport)
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		conn.(*idleDeadlineConn).idle = interval
		return conn, nil
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestCLIClientResponseBodyIdleTimeout(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "https"}[secure], func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "[")
				w.(http.Flusher).Flush()
				<-release
			}))
			if secure {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			defer close(release)
			client := shortIdleClient(t, server.URL, 100*time.Millisecond)
			if secure {
				client.http.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started := time.Now()
			if _, err := client.ListWorkspaces(ctx); err == nil {
				t.Fatal("incomplete response succeeded")
			}
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("stalled body waited for whole request deadline: %v", elapsed)
			}
		})
	}
}

func TestCLIImportStopsWhenServerDoesNotReadBody(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	client := shortIdleClient(t, server.URL, 100*time.Millisecond)
	body := bytes.Repeat([]byte("x"), 16<<20)
	sum := sha256.Sum256(body)
	id := hex.EncodeToString(sum[:])
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	_, err := client.ImportWorkspace(ctx, "stalled", func(sink BlobSink) (Manifest, error) {
		return Manifest{}, sink.Submit(ctx, id, body, int64(len(body)))
	})
	if err == nil || time.Since(started) >= time.Second {
		t.Fatalf("stalled upload was not bounded by idle timeout: %v after %v", err, time.Since(started))
	}
}

func TestCLIImportServerBoundsStalledBodyAndReleasesName(t *testing.T) {
	for _, mode := range []string{"partial-upload", "unfinished-epilogue", "early-rejection"} {
		t.Run(mode, func(t *testing.T) {
			service, _ := serviceFixture(t)
			ctx := context.Background()
			existing := mode == "early-rejection"
			if existing {
				if _, err := service.CreateWorkspace(ctx, "stalled"); err != nil {
					t.Fatal(err)
				}
			}
			handler := NewHandler(service, HandlerOptions{}).(*serverHandler)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.cliImportRouteWithIdleTimeout(w, r, 100*time.Millisecond)
			}))
			defer server.Close()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			multipartWriter := multipart.NewWriter(writer)
			request, err := http.NewRequest("POST", server.URL+"?name=stalled", reader)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
			// Start a part but never finish it or the HTTP body. The server
			// must release its import lock without relying on client close.
			go func() {
				part, err := multipartWriter.CreateFormField("manifest")
				if err == nil && mode == "unfinished-epilogue" {
					_ = json.NewEncoder(part).Encode(Manifest{Entries: map[string]ManifestEntry{"/": {Type: "dir", Mode: 0755}}})
					_ = multipartWriter.Close()
				}
			}()
			done := make(chan error, 1)
			go func() {
				response, err := http.DefaultClient.Do(request)
				if err == nil {
					_, err = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if response.StatusCode != http.StatusBadRequest {
						err = errors.New("incomplete import returned unexpected status")
					}
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				_ = reader.Close()
				_ = writer.Close()
				t.Fatal("server hung while draining an incomplete import")
			}
			if existing {
				return
			}
			if _, err := service.GetWorkspace(ctx, "stalled"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete import published: %v", err)
			}
			if _, err := service.CreateWorkspace(ctx, "stalled"); err != nil {
				t.Fatalf("incomplete import retained its name lock: %v", err)
			}
		})
	}
}

func TestCLIImportServerAllowsActiveBodyBeyondIdleInterval(t *testing.T) {
	service, _ := serviceFixture(t)
	handler := NewHandler(service, HandlerOptions{}).(*serverHandler)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.cliImportRouteWithIdleTimeout(w, r, 200*time.Millisecond)
	}))
	defer server.Close()
	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	part, err := multipartWriter.CreateFormField("manifest")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(part).Encode(Manifest{Entries: map[string]ManifestEntry{"/": {Type: "dir", Mode: 0755}}}); err != nil {
		t.Fatal(err)
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	request, err := http.NewRequest("POST", server.URL+"?name=active", reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	go func() {
		data := body.Bytes()
		chunk := (len(data) + 7) / 8
		for len(data) > 0 {
			time.Sleep(50 * time.Millisecond)
			n := min(chunk, len(data))
			if _, err := writer.Write(data[:n]); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
			data = data[n:]
		}
		_ = writer.Close()
	}()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("active transfer failed: %d %s", response.StatusCode, data)
	}
	if _, err := service.GetWorkspace(context.Background(), "active"); err != nil {
		t.Fatalf("active import was not published: %v", err)
	}
}
