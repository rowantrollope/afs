package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/redis/go-redis/v9"
)

func configFilePath(file string) (string, error) {
	if file != "" {
		return file, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "afs-lite", "config.json"), nil
}

func defaultConfig() config {
	return config{Redis: "redis://localhost:6379/0", syncSettings: syncSettings{
		SyncFileSizeCapMB:        defaultSyncFileSizeCapMB,
		SyncWatcherQueueCapacity: defaultSyncWatcherQueueCapacity,
	}}
}

func configCommand(opts cliOptions, args []string) error {
	if len(args) == 0 {
		fmt.Print(commandUsage["config"])
		return nil
	}
	if len(args) != 3 || args[0] != "set" {
		return errors.New("usage: afs config set <key> <value>; run afs config --help")
	}
	if opts.redisURL != "" {
		return errors.New("--redis is a per-command override; use afs config set redis <url> to save a connection")
	}
	key, value := args[1], args[2]
	var encoded []byte
	switch key {
	case "redis":
		if _, err := redis.ParseURL(value); err != nil {
			return errors.New("invalid Redis URL (credentials redacted)")
		}
		encoded, _ = json.Marshal(value)
	case "sync.fileSizeCapMB", "sync.watcherQueueCapacity":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s must be an integer", key)
		}
		if key == "sync.watcherQueueCapacity" {
			if err := validateSyncWatcherQueueCapacity(n); err != nil {
				return err
			}
		} else if n < 0 || int64(n) > (1<<63-1)/(1024*1024) {
			return errors.New("sync.fileSizeCapMB must be between 0 and 8796093022207 (0 uses the default 2048)")
		}
		encoded, _ = json.Marshal(n)
	default:
		return errors.New("unknown configuration key; use redis, sync.fileSizeCapMB, or sync.watcherQueueCapacity")
	}
	file, err := configFilePath(opts.configPath)
	if err != nil {
		return err
	}
	if err = setConfigValue(file, key, encoded); err != nil {
		return err
	}
	a := app{options: opts}
	return a.output(map[string]string{"key": key, "path": file}, fmt.Sprintf("Set %s in %s\n", key, file))
}

func setConfigValue(file, key string, value json.RawMessage) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Serialize read/modify/write so simultaneous config commands retain both edits.
	lock, err := os.OpenFile(file+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if info, err := os.Lstat(file); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("configuration path must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = json.Marshal(defaultConfig())
	}
	if err != nil {
		return err
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(raw, &document); err != nil || document == nil {
		return fmt.Errorf("invalid configuration JSON in %s; existing file was not changed", file)
	}
	if field, nested := strings.CutPrefix(key, "sync."); nested {
		settings := map[string]json.RawMessage{}
		if existing, ok := document["sync"]; ok {
			if err = json.Unmarshal(existing, &settings); err != nil || settings == nil {
				return errors.New("configuration sync must be a JSON object; existing file was not changed")
			}
		}
		settings[field] = value
		document["sync"], err = json.Marshal(settings)
	} else {
		document[key] = value
	}
	if err != nil {
		return err
	}
	raw, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	cfg := defaultConfig()
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("invalid configuration JSON in %s; existing file was not changed", file)
	}
	if _, err = redis.ParseURL(cfg.Redis); err != nil {
		return errors.New("invalid Redis URL (credentials redacted)")
	}
	if err = validateSyncWatcherQueueCapacity(cfg.SyncWatcherQueueCapacity); err != nil {
		return err
	}
	// CreateTemp uses 0600; rename publishes the whole file without partial JSON.
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
