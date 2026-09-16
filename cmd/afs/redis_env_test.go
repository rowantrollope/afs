package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedisEnvironmentPrecedence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"redis":"redis://saved:saved-password@localhost:6379/1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AFS_REDIS_URL", "redis://environment:url-password@localhost:6380/2")
	t.Setenv("AFS_REDIS_PASSWORD", "raw:@/?#% password")
	for _, tc := range []struct {
		override, username, addr string
		db                       int
	}{
		{"", "environment", "localhost:6380", 2},
		{"redis://flag:flag-password@localhost:6381/3", "flag", "localhost:6381", 3},
	} {
		cfg, err := readConfig(file, tc.override)
		if err != nil {
			t.Fatal(err)
		}
		opts := buildRedisOptions(cfg, 8)
		if opts.Username != tc.username || opts.Addr != tc.addr || opts.DB != tc.db || opts.Password != "raw:@/?#% password" {
			t.Fatal("environment/flag precedence or literal password handling failed")
		}
		serialized, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(serialized), "raw:@") || strings.Contains(redisConnectionError(cfg).Error(), "raw:@") {
			t.Fatal("environment password leaked into configuration or output")
		}
	}
	t.Setenv("AFS_REDIS_URL", "")
	t.Setenv("AFS_REDIS_PASSWORD", "")
	cfg, err := readConfig(file, "")
	if err != nil {
		t.Fatal(err)
	}
	if opts := buildRedisOptions(cfg, 8); opts.Username != "saved" || opts.Password != "" {
		t.Fatal("empty URL should fall back; empty password should override")
	}
	if err := os.Unsetenv("AFS_REDIS_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	if buildRedisOptions(cfg, 8).Password != "saved-password" {
		t.Fatal("unset password lost URL fallback")
	}
	t.Setenv("AFS_REDIS_URL", "redis://user:secret@/bad-db")
	if _, err := readConfig(file, ""); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid environment URL must fail without credentials")
	}
	if _, err := readConfig(file, "redis://localhost:6379/0"); err != nil {
		t.Fatal("flag must override invalid environment URL")
	}
}

func TestConfigPasswordHint(t *testing.T) {
	for _, structured := range []bool{false, true} {
		file := filepath.Join(t.TempDir(), "config.json")
		out, err := captureStdout(t, func() error {
			return configCommand(cliOptions{configPath: file, json: structured}, []string{"set", "redis", "redis://user:saved-secret@localhost/0"})
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "saved-secret") || strings.Contains(out, "AFS_REDIS_PASSWORD") == structured {
			t.Fatal("hint or redaction incorrect")
		}
		if structured && !json.Valid([]byte(out)) {
			t.Fatal("JSON output changed")
		}
	}
}
