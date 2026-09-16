package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

func readConfig(file, override string) (config, error) {
	cfg := config{Redis: "redis://localhost:6379/0"}
	explicit := file != ""
	file, err := configFilePath(file)
	if err != nil {
		return cfg, err
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

func redisConnectionError(cfg config) error {
	return fmt.Errorf("Cannot connect to Redis on %s\n\nPoint to your Redis server using \"afs --redis <url> list\".", redisDisplay(cfg))
}
