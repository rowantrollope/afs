// afs-mcp is an optional file adapter, separate from the ordinary AFS CLI.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rowantrollope/afs/internal/mcpfiles"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "afs-mcp:", err)
		os.Exit(1)
	}
}

func loopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("--listen must use a numeric loopback address")
	}
	return nil
}

func authorizedHandler(server *mcp.Server, token string) http.Handler {
	expected := sha256.Sum256([]byte("Bearer " + token))
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 8 << 20})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="afs-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		host := r.Host
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		// Native/server clients omit Origin. Browser clients must be same-origin;
		// retain the SDK's localhost Host protection against DNS rebinding.
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Scheme != "http" || u.Host != r.Host || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		handler.ServeHTTP(w, r)
	})
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("afs-mcp", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var cfg mcpfiles.CLI
	flags.StringVar(&cfg.Workspace, "workspace", "", "required workspace name (no per-call override)")
	flags.StringVar(&cfg.Directory, "directory", "", "required existing server-managed folder-sync directory")
	flags.StringVar(&cfg.Binary, "afs-bin", "", "required absolute path to afs executable")
	flags.StringVar(&cfg.Config, "config", "", "required absolute path to server's AFS configuration")
	flags.StringVar(&cfg.StateDir, "state-dir", "", "required absolute AFS state directory used by mount")
	flags.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "AFS verification timeout, 1s to 5m")
	transport := flags.String("transport", "stdio", "stdio or http")
	listen := flags.String("listen", "127.0.0.1:8092", "HTTP loopback listener; endpoint /mcp")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if strings.TrimSpace(cfg.Workspace) == "" {
		return errors.New("--workspace is required")
	}
	for _, p := range []string{cfg.Directory, cfg.Binary, cfg.Config, cfg.StateDir} {
		if !filepath.IsAbs(p) {
			return errors.New("--directory, --afs-bin, --config and --state-dir must be explicit absolute paths")
		}
	}
	canonical, err := filepath.EvalSymlinks(cfg.Directory)
	if err != nil {
		return errors.New("configured mount directory does not exist")
	}
	cfg.Directory = canonical
	if cfg.Directory == "/" {
		return errors.New("filesystem root cannot be an MCP workspace")
	}
	if cfg.Timeout < time.Second || cfg.Timeout > 5*time.Minute {
		return errors.New("--timeout must be between 1s and 5m")
	}
	if *transport != "stdio" && *transport != "http" {
		return errors.New("--transport must be stdio or http")
	}
	var listener net.Listener
	token := os.Getenv("AFS_MCP_TOKEN")
	if *transport == "http" {
		if len(token) < 32 || strings.ContainsAny(token, "\r\n \t") {
			return errors.New("HTTP requires AFS_MCP_TOKEN with at least 32 non-whitespace bytes")
		}
		if err := loopbackAddress(*listen); err != nil {
			return err
		}
		var err error
		listener, err = net.Listen("tcp", *listen)
		if err != nil {
			return err
		}
		defer listener.Close()
	}
	folder, err := mcpfiles.Open(cfg.Directory, cfg.Workspace, cfg)
	if err != nil {
		return errors.New("cannot open configured mount directory")
	}
	defer folder.Close()
	if err := folder.Ready(ctx); err != nil {
		return fmt.Errorf("workspace not ready: %w", err)
	}
	server := folder.Server()
	if *transport == "stdio" {
		// No diagnostics go to stdout, which is exclusively MCP framing.
		return server.Run(ctx, &mcp.StdioTransport{MaxLineLength: 8 << 20})
	}
	httpServer := &http.Server{Handler: authorizedHandler(server, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	fmt.Fprintf(os.Stderr, "afs-mcp ready at http://%s/mcp for workspace %q\n", listener.Addr(), cfg.Workspace)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
			return err
		}
		return nil
	}
}
