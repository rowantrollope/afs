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

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/logging"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/uistatic"
	"github.com/rowantrollope/afs/internal/version"
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
	redisConfig, err := redisOptions(options.redisURL, os.LookupEnv)
	if err != nil {
		return err
	}
	connectionURL, err := redisConnectionURL(options.redisURL, redisConfig)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(redisConfig)
	defer rdb.Close()
	ready, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = rdb.Ping(ready).Err()
	cancel()
	if err != nil {
		return errors.New("cannot connect to Redis; check AFS_REDIS_URL and AFS_REDIS_PASSWORD")
	}
	assets := uistatic.Assets()
	handler := controlplane.NewHandler(controlplane.NewService(controlplane.NewStore(rdb)), controlplane.HandlerOptions{
		DatabaseID: "local", DatabaseName: "Redis", AuthToken: options.token, RedisURL: connectionURL,
		Version: version.Short(), UI: assets, AllowedOrigins: options.origins,
	})
	handler = guardUnauthenticatedLoopbackHost(handler, options.token)
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
