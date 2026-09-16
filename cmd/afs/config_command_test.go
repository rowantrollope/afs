package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestConfigSetCreatesDefaultsOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := captureStdout(t, func() error {
		return runCLI([]string{"config", "set", "redis", "rediss://user:secret@offline.invalid:6380/4"})
	})
	if err != nil || strings.Contains(out, "secret") {
		t.Fatalf("output=%q error=%v", out, err)
	}
	file, _ := configFilePath("")
	cfg, err := readConfig(file, "")
	if err != nil || cfg.Redis != "rediss://user:secret@offline.invalid:6380/4" || cfg.SyncFileSizeCapMB != 2048 || cfg.SyncWatcherQueueCapacity != 1024 {
		t.Fatalf("configuration did not retain URL and defaults: %v", err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions: %v %v", info, err)
	}
}

func TestConfigSetPreservesFieldsAndRepairsURL(t *testing.T) {
	file := filepath.Join(t.TempDir(), "custom.json")
	before := `{"redis":"invalid","extra":{"number":9007199254740993},"sync":{"fileSizeCapMB":17,"future":true}}`
	if err := os.WriteFile(file, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return runCLI([]string{"--json", "--config", file, "config", "set", "redis", "redis://localhost:6380/3"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil || result["key"] != "redis" || result["path"] != file || len(result) != 2 {
		t.Fatalf("result=%q err=%v", out, err)
	}
	_, err = captureStdout(t, func() error {
		return runCLI([]string{"--config", file, "config", "set", "sync.watcherQueueCapacity", "7"})
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	var got map[string]json.RawMessage
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got["extra"]), "9007199254740993") || !strings.Contains(string(got["sync"]), `"future": true`) {
		t.Fatalf("unrelated fields changed: %s", raw)
	}
	cfg, err := readConfig(file, "")
	if err != nil || cfg.SyncFileSizeCapMB != 17 || cfg.SyncWatcherQueueCapacity != 7 {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
}

func TestConfigSetRejectsWithoutChangingFile(t *testing.T) {
	for _, tc := range []struct{ name, original, key, value string }{
		{"url", `{"redis":"redis://localhost"}`, "redis", "redis://user:secret@/bad-db"},
		{"unknown", `{"redis":"redis://localhost"}`, "unknown", "secret"},
		{"negative", `{"redis":"redis://localhost"}`, "sync.fileSizeCapMB", "-1"},
		{"overflow", `{"redis":"redis://localhost"}`, "sync.fileSizeCapMB", "8796093022208"},
		{"fraction", `{"redis":"redis://localhost"}`, "sync.fileSizeCapMB", "1.5"},
		{"queue", `{"redis":"redis://localhost"}`, "sync.watcherQueueCapacity", "1048577"},
		{"malformed", `{"redis":"secret"`, "redis", "redis://localhost"},
		{"null", `null`, "redis", "redis://localhost"},
		{"nested", `{"sync":[]}`, "sync.fileSizeCapMB", "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(file, []byte(tc.original), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := captureStdout(t, func() error { return runCLI([]string{"--config", file, "config", "set", tc.key, tc.value}) })
			if err == nil || out != "" || strings.Contains(err.Error(), "secret") {
				t.Fatalf("output=%q err=%v", out, err)
			}
			raw, _ := os.ReadFile(file)
			if string(raw) != tc.original {
				t.Fatalf("file changed: %s", raw)
			}
		})
	}
}

func TestConfigSetConcurrentEdits(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	var wg sync.WaitGroup
	for key, value := range map[string]string{"redis": `"redis://localhost:6380/3"`, "sync.fileSizeCapMB": "99", "sync.watcherQueueCapacity": "7"} {
		wg.Add(1)
		go func(key, value string) {
			defer wg.Done()
			if err := setConfigValue(file, key, json.RawMessage(value)); err != nil {
				t.Error(err)
			}
		}(key, value)
	}
	wg.Wait()
	cfg, err := readConfig(file, "")
	if err != nil || cfg.Redis != "redis://localhost:6380/3" || cfg.SyncFileSizeCapMB != 99 || cfg.SyncWatcherQueueCapacity != 7 {
		t.Fatalf("concurrent edit lost: %+v %v", cfg, err)
	}
}

func TestConfigExampleMatchesDefaults(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, defaultConfig()) {
		t.Fatalf("example differs from defaults: %+v", cfg)
	}
}

func TestConfigSetSymlinkAndHelp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	original := []byte(`{"redis":"redis://localhost"}`)
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := setConfigValue(link, "redis", json.RawMessage(`"redis://localhost:6380"`)); err == nil {
		t.Fatal("symlink replaced")
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != string(original) {
		t.Fatal("symlink target changed")
	}
	for _, args := range [][]string{{"config"}, {"config", "--help"}, {"config", "set", "--help"}} {
		out, err := captureStdout(t, func() error {
			return runCLI(append([]string{"--config", "/missing/file", "--redis", "invalid"}, args...))
		})
		if err != nil || !strings.Contains(out, "sync.fileSizeCapMB") {
			t.Fatalf("help=%q err=%v", out, err)
		}
	}
}
