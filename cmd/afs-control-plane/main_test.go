package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestServerBindingRequiresTokenOutsideLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1:8091", "[::1]:8091", "localhost:8091", "0.0.0.0:8091", ":8091", "192.0.2.1:8091"} {
		for _, token := range []string{"", "private-test-token"} {
			t.Run(host+"/token="+strconv.FormatBool(token != ""), func(t *testing.T) {
				_, err := parseOptions([]string{"--listen", host}, func(key string) string {
					if key == "AFS_CONTROL_PLANE_TOKEN" {
						return token
					}
					return ""
				}, io.Discard)
				loopback := strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "[::1]") || strings.HasPrefix(host, "localhost")
				if (err == nil) != (loopback || token != "") {
					t.Fatalf("unexpected binding validation: %v", err)
				}
			})
		}
	}
}
func TestRedisPasswordOverrideAndRedaction(t *testing.T) {
	lookup := func(string) (string, bool) { return "override", true }
	options, err := redisOptions("redis://default:original@127.0.0.1:1234/2", lookup)
	if err != nil || options.Password != "override" || options.DB != 2 {
		t.Fatalf("Redis options incorrect: %v", err)
	}
	_, err = redisOptions("redis://user:secret@[invalid", lookup)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid URL errors must not disclose credentials")
	}
	options, err = redisOptions("redis://default:original@127.0.0.1:1234/2", func(string) (string, bool) { return "", false })
	if err != nil || options.Password != "original" {
		t.Fatal("URL password not retained")
	}
	options, err = redisOptions("redis://default:original@127.0.0.1:1234/2", func(string) (string, bool) { return "", true })
	if err != nil || options.Password != "" {
		t.Fatal("explicit empty override not honored")
	}
}

func TestHelpDoesNotPrintRedisCredentials(t *testing.T) {
	var output bytes.Buffer
	_, err := parseOptions([]string{"--help"}, func(key string) string {
		if key == "AFS_REDIS_URL" {
			return "redis://default:private-password@127.0.0.1:6379/0"
		}
		if key == "AFS_MIGRATE_API_KEYS_FROM" {
			return "redis://default:private-migration-password@127.0.0.1:6379/0"
		}
		if key == "AFS_CONTROL_PLANE_TOKEN" {
			return "private-team-token"
		}
		if key == "AFS_METADATA_URL" {
			return "postgresql://private-user:private-metadata-password@metadata.example/afs"
		}
		return ""
	}, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
	for _, secret := range []string{"private-password", "private-migration-password", "private-team-token", "private-metadata-password", "private-user"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("help disclosed environment credentials")
		}
	}
	for _, option := range []string{"-metadata-file", "-databases-file", "-migrate-api-keys-from", "-hosted", "AFS_METADATA_URL"} {
		if !strings.Contains(output.String(), option) {
			t.Fatalf("help omitted %s", option)
		}
	}
}

func TestHostedOptionsRequireAuthAndSharedMetadata(t *testing.T) {
	for _, args := range [][]string{{"--hosted"}, {"--hosted=false"}} {
		for _, missing := range []string{"AFS_CONTROL_PLANE_TOKEN", "AFS_METADATA_URL"} {
			environment := map[string]string{
				"VERCEL": "1", "PORT": "3000",
				"AFS_CONTROL_PLANE_TOKEN": "private-team-token",
				"AFS_METADATA_URL":        "postgresql://user:password@metadata.example/afs",
			}
			delete(environment, missing)
			_, err := parseOptions(args, func(key string) string { return environment[key] }, io.Discard)
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("hosted mode accepted missing %s with args %v: %v", missing, args, err)
			}
		}
	}
}

func TestHostedPortAndExplicitMode(t *testing.T) {
	for _, port := range []string{"", "0", "-1", "65536", "+3000", " 3000", "private-invalid-port", "3000", "65535"} {
		environment := map[string]string{
			"PORT": port, "AFS_CONTROL_PLANE_TOKEN": "test-token",
			"AFS_METADATA_URL": "postgres://user:password@metadata.example/afs",
		}
		options, err := parseOptions([]string{"--hosted", "--listen", "127.0.0.1:8091"}, func(key string) string { return environment[key] }, io.Discard)
		if port == "3000" || port == "65535" {
			if err != nil || !options.hosted || options.listen != "0.0.0.0:"+port || options.metadataFile != "" || options.databasesFile != "" {
				t.Fatalf("hosted port %q did not override local listener or selected local files: %v", port, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "PORT must") || strings.Contains(err.Error(), "private-invalid-port") {
			t.Fatalf("invalid hosted port should return a fixed error: %v", err)
		}
	}
}

func TestPostgresMetadataSelectionAndRedaction(t *testing.T) {
	for _, metadataURL := range []string{
		"postgresql://user:private-password@metadata.example/afs",
		"postgres://user:private-password@metadata.example/afs",
		"sqlite:///private-password.sqlite",
		"postgresql://user:private-password@[invalid",
		"private-password",
	} {
		options, err := parseOptions(nil, func(key string) string {
			if key == "AFS_METADATA_URL" {
				return metadataURL
			}
			return ""
		}, io.Discard)
		valid := strings.Contains(metadataURL, "@metadata.example/")
		if valid {
			if err != nil || options.metadataURL != metadataURL || options.metadataFile != "" || options.databasesFile != "" {
				t.Fatalf("PostgreSQL selection failed or implicitly selected local files: %v", err)
			}
		} else if err == nil || strings.Contains(err.Error(), "private-password") {
			t.Fatal("invalid metadata URL did not return a redacted error")
		}
	}
}

func TestMetadataOptionsDefaultWithoutRedis(t *testing.T) {
	options, err := parseOptions(nil, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.redisURL != "" || options.migrateAPIKeysFrom != "" {
		t.Fatal("startup must not select or migrate a Redis connection implicitly")
	}
	if filepath.Base(options.metadataFile) != "control-plane.sqlite" || filepath.Base(options.databasesFile) != "databases.json" {
		t.Fatalf("unexpected metadata/import paths: %+v", options)
	}
}

func TestMetadataOptionsOverrides(t *testing.T) {
	environment := map[string]string{
		"AFS_METADATA_FILE":         "/tmp/environment.sqlite",
		"AFS_DATABASES_FILE":        "/tmp/environment.json",
		"AFS_MIGRATE_API_KEYS_FROM": "redis://127.0.0.1:6001/0",
		"AFS_REDIS_URL":             "redis://127.0.0.1:6002/0",
	}
	getenv := func(key string) string { return environment[key] }
	options, err := parseOptions(nil, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.metadataFile != environment["AFS_METADATA_FILE"] || options.databasesFile != environment["AFS_DATABASES_FILE"] || options.migrateAPIKeysFrom != environment["AFS_MIGRATE_API_KEYS_FROM"] || options.redisURL != environment["AFS_REDIS_URL"] {
		t.Fatal("environment overrides were not honored")
	}
	options, err = parseOptions([]string{"--metadata-file", "/tmp/flag.sqlite", "--databases-file", "/tmp/flag.json", "--migrate-api-keys-from", "redis://127.0.0.1:6003/0", "--redis", ""}, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if options.metadataFile != "/tmp/flag.sqlite" || options.databasesFile != "/tmp/flag.json" || options.migrateAPIKeysFrom != "redis://127.0.0.1:6003/0" || options.redisURL != "" {
		t.Fatal("explicit flags did not override the environment")
	}
}

func TestBootstrapURLPreservesEffectiveRedisOptions(t *testing.T) {
	for _, raw := range []string{"rediss://default:original@redis.example:6380/3?read_timeout=7s&protocol=3", "redis://default:original@[::1]:6379/0?db=5", "unix://default:original@/tmp/disposable-redis.sock?db=7"} {
		for _, override := range []string{"secret:/?#@% value", ""} {
			options, err := redisOptions(raw, func(string) (string, bool) { return override, true })
			if err != nil {
				t.Fatal(err)
			}
			connection, err := redisConnectionURL(raw, options)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := redis.ParseURL(connection)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Password != override || parsed.Username != options.Username || parsed.Addr != options.Addr || parsed.DB != options.DB || (parsed.TLSConfig == nil) != (options.TLSConfig == nil) || parsed.ReadTimeout != options.ReadTimeout || parsed.Protocol != options.Protocol {
				t.Fatal("bootstrap changed effective Redis options")
			}
		}
	}
}
