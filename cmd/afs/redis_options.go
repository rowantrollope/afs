package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func redactConnectionError(err error, cfg config) string {
	message := err.Error()
	message = strings.ReplaceAll(message, cfg.Redis, redisDisplay(cfg))
	if u, e := url.Parse(cfg.Redis); e == nil && u.User != nil {
		if p, ok := u.User.Password(); ok && p != "" {
			message = strings.ReplaceAll(message, p, "[redacted]")
		}
	}
	return message
}
