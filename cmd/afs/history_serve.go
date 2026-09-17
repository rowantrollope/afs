package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
)

const serveCommandUsage = `Usage: afs serve [--listen 127.0.0.1:8091] [--database-id local] [--allow-origin URL]

Serve the original UI's file-history, version content, diff, restore, undelete,
versioning, and path activity APIs for the configured Redis database.
The listener must use a loopback address. Repeat --allow-origin for trusted UI
origins, for example http://localhost:5173. No UI or background service is installed.
Set the original UI's VITE_AFS_API_BASE_URL to the displayed address.
`

func (a *app) serveHistoryCommand(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := f.String("listen", "127.0.0.1:8091", "loopback listen address")
	database := f.String("database-id", "local", "configured database alias")
	var origins historyGlobs
	f.Var(&origins, "allow-origin", "trusted UI origin")
	pos, err := parseCommandFlags(f, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errors.New(serveCommandUsage)
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("--listen must use a loopback address")
	}
	if err := controlplane.ValidateName("database", *database); err != nil {
		return err
	}
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return fmt.Errorf("--allow-origin must be an HTTP(S) origin without a path")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.connect(ctx); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: controlplane.NewFileHistoryHandler(a.service, controlplane.FileHistoryHTTPOptions{DatabaseID: *database, AllowedOrigins: origins}),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	address := "http://" + listener.Addr().String()
	if err := a.output(map[string]string{"url": address, "database_id": *database}, fmt.Sprintf("File history API: %s\nDatabase alias: %s\n", address, *database)); err != nil {
		return err
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
