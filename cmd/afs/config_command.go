package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	case "controlPlane.url":
		if err := (managedSettings(value, "")).Validate(); err != nil {
			return err
		}
		encoded, _ = json.Marshal(strings.TrimRight(value, "/"))
	case "controlPlane.token":
		if value != "-" {
			return errors.New("read the control-plane token from stdin: afs config set controlPlane.token -")
		}
		var err error
		value, err = readControlPlaneToken(os.Stdin)
		if err != nil {
			return err
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
		return errors.New("unknown configuration key; use redis, controlPlane.url, controlPlane.token, sync.fileSizeCapMB, or sync.watcherQueueCapacity")
	}
	file, err := configFilePath(opts.configPath)
	if err != nil {
		return err
	}
	if err = setConfigValue(file, key, encoded); err != nil {
		return err
	}
	a := app{options: opts}
	message := fmt.Sprintf("Set %s in %s\n", key, file)
	if key == "redis" {
		connection, _ := redis.ParseURL(value) // Validated above.
		if connection.Password != "" {
			message += "Password saved in config. Set AFS_REDIS_PASSWORD to override it at runtime.\n"
		}
	}
	return a.output(map[string]string{"key": key, "path": file}, message)
}

func setConfigValue(file, key string, value json.RawMessage) error {
	return updateConfigDocument(file, func(document map[string]json.RawMessage) error {
		if key == "controlPlane.url" {
			var previous struct {
				URL string `json:"url"`
			}
			if raw, ok := document["controlPlane"]; ok {
				if err := json.Unmarshal(raw, &previous); err != nil {
					return errors.New("invalid controlPlane configuration")
				}
			}
			var endpoint string
			if err := json.Unmarshal(value, &endpoint); err != nil {
				return errors.New("controlPlane.url must be a string")
			}
			if normalizedControlPlaneURL(previous.URL) != normalizedControlPlaneURL(endpoint) {
				if err := setConfigDocumentValue(document, "controlPlane.token", json.RawMessage(`""`)); err != nil {
					return err
				}
			}
		}
		if err := setConfigDocumentValue(document, key, value); err != nil {
			return err
		}
		raw, err := json.Marshal(document)
		if err != nil {
			return err
		}
		cfg := defaultConfig()
		if err = json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("invalid configuration JSON in %s; existing file was not changed", file)
		}
		if managedConfig(cfg).URL == "" {
			if _, err = redis.ParseURL(cfg.Redis); err != nil {
				return errors.New("invalid Redis URL (credentials redacted)")
			}
		}
		if err = managedConfig(cfg).Validate(); err != nil {
			return err
		}
		return validateSyncWatcherQueueCapacity(cfg.SyncWatcherQueueCapacity)
	})
}

func readControlPlaneToken(input io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 16385))
	if err != nil || len(raw) > 16384 {
		return "", errors.New("cannot read control-plane token from stdin (maximum 16384 bytes)")
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("control-plane token must be one line")
	}
	return strings.TrimSpace(value), nil
}

func setConfigDocumentValue(document map[string]json.RawMessage, key string, value json.RawMessage) error {
	if group, field, nested := strings.Cut(key, "."); nested {
		settings := map[string]json.RawMessage{}
		if existing, ok := document[group]; ok {
			if err := json.Unmarshal(existing, &settings); err != nil {
				return errors.New("configuration section must be a JSON object; existing file was not changed")
			}
			if settings == nil && group == "controlPlane" {
				settings = map[string]json.RawMessage{}
			}
			if settings == nil {
				return errors.New("configuration section must be a JSON object; existing file was not changed")
			}
		}
		settings[field] = value
		raw, err := json.Marshal(settings)
		if err != nil {
			return err
		}
		document[group] = raw
	} else {
		document[key] = value
	}
	return nil
}

func readConfigDocument(file string) (map[string]json.RawMessage, error) {
	if info, err := os.Lstat(file); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("configuration path must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = json.Marshal(defaultConfig())
	}
	if err != nil {
		return nil, err
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(raw, &document); err != nil || document == nil {
		return nil, fmt.Errorf("invalid configuration JSON in %s; existing file was not changed", file)
	}
	return document, nil
}

// Hold one lock and publish one rename for edits that must stay together, such
// as a control-plane endpoint and its token. Unrelated raw JSON is retained.
func updateConfigDocument(file string, edit func(map[string]json.RawMessage) error) error {
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
	document, err := readConfigDocument(file)
	if err != nil {
		return err
	}
	if err := edit(document); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
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
