// afs-control-plane serves the optional management API and existing AFS UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/logging"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/uistatic"
	"github.com/rowantrollope/afs/internal/version"
	_ "modernc.org/sqlite"
)

func run(ctx context.Context, args []string) error {
	options, err := parseOptions(args, os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if options.showVersion {
		fmt.Println(version.String())
		return nil
	}
	metadata, err := openMetadata(ctx, options)
	if err != nil {
		return err
	}
	defer metadata.Close()
	var connectionURL string
	_, _, initialized, err := metadata.LoadProfiles(ctx)
	if err != nil {
		return err
	}
	if !initialized && options.redisURL != "" {
		redisConfig, err := redisOptions(options.redisURL, os.LookupEnv)
		if err != nil {
			return err
		}
		connectionURL, err = redisConnectionURL(options.redisURL, redisConfig)
		if err != nil {
			return err
		}
	}
	assets := uistatic.Assets()
	var streamDuration time.Duration
	if options.hosted {
		streamDuration = 240 * time.Second
	}
	databaseHandler, err := controlplane.NewMetadataDatabaseHandler(metadata, controlplane.HandlerOptions{
		AuthToken: options.token, StreamDuration: streamDuration,
		Version: version.Short(), UI: assets, AllowedOrigins: options.origins,
	}, options.databasesFile, connectionURL)
	if err != nil {
		return err
	}
	defer databaseHandler.Close()
	if options.migrateAPIKeysFrom != "" {
		legacyOptions, err := redisOptions(options.migrateAPIKeysFrom, os.LookupEnv)
		if err != nil {
			return errors.New("invalid legacy API-key Redis URL")
		}
		legacy := redis.NewClient(legacyOptions)
		migration, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = controlplane.MigrateLegacyAPIKeys(migration, metadata, legacy)
		cancel()
		_ = legacy.Close()
		if err != nil {
			return errors.New("cannot migrate legacy API keys; check the source Redis connection and AFS_REDIS_PASSWORD; the source has not been changed")
		}
	}
	handler := guardUnauthenticatedLoopbackHost(databaseHandler, options.token)
	server := &http.Server{Addr: options.listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	listener, err := net.Listen("tcp", options.listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	if assets == nil {
		log.Print("UI assets absent: serving API only; use make control-plane to include the UI")
	}
	log.Printf("AFS control plane listening on %s", listener.Addr())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func openMetadata(ctx context.Context, options serverOptions) (*controlplane.MetadataStore, error) {
	if options.metadataURL != "" {
		startup, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		metadata, err := controlplane.OpenPostgresMetadataStore(startup, options.metadataURL)
		if err != nil {
			// Driver errors can include DSN credentials. Keep them out of logs.
			return nil, errors.New("cannot open PostgreSQL metadata; check AFS_METADATA_URL and database connectivity")
		}
		return metadata, nil
	}
	if options.hosted {
		return nil, errors.New("AFS_METADATA_URL is required in hosted mode; local SQLite is not supported")
	}
	return controlplane.OpenMetadataStore(options.metadataFile)
}

func main() {
	log.SetFlags(0)
	logging.Disable()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Printf("afs-control-plane: %v", err)
		os.Exit(1)
	}
}
