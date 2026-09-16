package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
)

func readConfig(file, override string) (config, error) {
	cfg := config{Redis: "redis://localhost:6379/0"}
	explicit := file != ""
	if !explicit {
		home, err := os.UserHomeDir()
		if err != nil {
			return cfg, err
		}
		file = filepath.Join(home, ".config", "afs-lite", "config.json")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		if explicit || !errors.Is(err, os.ErrNotExist) {
			return cfg, err
		}
	} else {
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("invalid configuration JSON in %s", file)
		}
	}
	if override != "" {
		cfg.Redis = override
	}
	if _, err = redis.ParseURL(cfg.Redis); err != nil {
		return cfg, errors.New("invalid Redis URL (credentials redacted)")
	}
	if err = validateSyncWatcherQueueCapacity(cfg.SyncWatcherQueueCapacity); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func buildRedisOptions(cfg config, poolSize int) *redis.Options {
	opts, err := redis.ParseURL(cfg.Redis)
	if err != nil {
		panic("buildRedisOptions called before Redis URL validation")
	}
	opts.PoolSize = poolSize
	opts.ContextTimeoutEnabled = true
	opts.ReadTimeout = 30 * time.Second
	opts.WriteTimeout = 30 * time.Second
	if opts.TLSConfig != nil {
		opts.TLSConfig.MinVersion = tls.VersionTLS12
	}
	return opts
}

func redisDisplay(cfg config) string {
	u, err := url.Parse(cfg.Redis)
	if err != nil {
		return "(invalid URL)"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// The endpoint comes from parsed options, so it contains no URL credentials.
// Describe the failure without exposing driver retries or nested dial errors.
func redisConnectionError(endpoint string, err error) error {
	reason := "check that Redis is running and reachable"
	var dnsErr *net.DNSError
	var netErr net.Error
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.Is(err, context.Canceled):
		reason = "connection canceled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		reason = "connection timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		reason = "connection refused"
	case errors.As(err, &dnsErr):
		reason = "hostname lookup failed"
	case errors.As(err, &certErr):
		reason = "TLS certificate verification failed"
	case strings.HasPrefix(err.Error(), "WRONGPASS"), strings.HasPrefix(err.Error(), "NOAUTH"):
		reason = "authentication failed; check your Redis credentials"
	case errors.Is(err, io.EOF), errors.Is(err, syscall.ECONNRESET):
		reason = "connection closed by the server"
	}
	return fmt.Errorf("Failed to connect to Redis on %s: %s", endpoint, reason)
}
