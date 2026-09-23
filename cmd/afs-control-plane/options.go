package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type serverOptions struct {
	listen, redisURL, token string
	databasesFile           string
	metadataFile            string
	metadataURL             string
	migrateAPIKeysFrom      string
	origins                 stringList
	showVersion             bool
	hosted                  bool
	secureCookies           bool
}
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func parseOptions(args []string, getenv func(string) string, output io.Writer) (serverOptions, error) {
	options := serverOptions{listen: "127.0.0.1:8091", token: strings.TrimSpace(getenv("AFS_CONTROL_PLANE_TOKEN"))}
	if value := getenv("AFS_CONTROL_PLANE_LISTEN"); value != "" {
		options.listen = value
	}
	if value := getenv("AFS_REDIS_URL"); value != "" {
		options.redisURL = value
	}
	options.databasesFile = getenv("AFS_DATABASES_FILE")
	options.metadataFile = getenv("AFS_METADATA_FILE")
	options.metadataURL = strings.TrimSpace(getenv("AFS_METADATA_URL"))
	options.migrateAPIKeysFrom = getenv("AFS_MIGRATE_API_KEYS_FROM")
	var secureCookiesErr error
	if value := strings.TrimSpace(getenv("AFS_CONTROL_PLANE_SECURE_COOKIES")); value != "" {
		options.secureCookies, secureCookiesErr = strconv.ParseBool(value)
	}
	vercel := getenv("VERCEL") == "1"
	flags := flag.NewFlagSet("afs-control-plane", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&options.listen, "listen", options.listen, "HTTP listen address (AFS_CONTROL_PLANE_LISTEN)")
	flags.StringVar(&options.redisURL, "redis", options.redisURL, "Optional initial Redis connection (AFS_REDIS_URL); password override: AFS_REDIS_PASSWORD")
	// flag defaults appear in help; the selected URL may contain a password.
	flags.Lookup("redis").DefValue = ""
	flags.StringVar(&options.metadataFile, "metadata-file", options.metadataFile, "Private SQLite metadata file (AFS_METADATA_FILE)")
	flags.StringVar(&options.databasesFile, "databases-file", options.databasesFile, "Legacy JSON connections file to import once (AFS_DATABASES_FILE)")
	flags.StringVar(&options.migrateAPIKeysFrom, "migrate-api-keys-from", options.migrateAPIKeysFrom, "Explicit legacy API-key Redis source (AFS_MIGRATE_API_KEYS_FROM); source is read-only")
	flags.Lookup("migrate-api-keys-from").DefValue = ""
	flags.Var(&options.origins, "allow-origin", "Allowed browser origin; repeat for multiple origins")
	flags.BoolVar(&options.showVersion, "version", false, "Print version")
	flags.BoolVar(&options.hosted, "hosted", vercel, "Require token, PostgreSQL AFS_METADATA_URL and PORT (automatic on Vercel)")
	flags.BoolVar(&options.secureCookies, "secure-cookies", options.secureCookies, "Require HTTPS browser origins and Secure session cookies behind a trusted TLS proxy (AFS_CONTROL_PLANE_SECURE_COOKIES)")
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: afs-control-plane [options]\n\nOptional AFS management API and UI. Files continue to flow directly to Redis.\nSet AFS_CONTROL_PLANE_TOKEN to require a bearer token; required off loopback.\nSet AFS_METADATA_URL for PostgreSQL instead of local SQLite. Hosted mode requires both.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, errors.New("unexpected positional argument")
	}
	if options.showVersion {
		return options, nil
	}
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "secure-cookies" {
			secureCookiesErr = nil
		}
	})
	if secureCookiesErr != nil {
		return options, errors.New("AFS_CONTROL_PLANE_SECURE_COOKIES must be a boolean")
	}
	// An explicit --hosted=false cannot bypass Vercel's required protections.
	options.hosted = options.hosted || vercel
	options.secureCookies = options.secureCookies || options.hosted
	if options.hosted {
		if options.token == "" {
			return options, errors.New("AFS_CONTROL_PLANE_TOKEN is required in hosted mode")
		}
		if options.metadataURL == "" {
			return options, errors.New("AFS_METADATA_URL must specify PostgreSQL in hosted mode; local SQLite is not supported")
		}
		port := getenv("PORT")
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || strings.Trim(port, "0123456789") != "" {
			return options, errors.New("PORT must be an integer between 1 and 65535 in hosted mode")
		}
		options.listen = net.JoinHostPort("0.0.0.0", strconv.Itoa(number))
	}
	if options.metadataURL != "" {
		address, err := url.Parse(options.metadataURL)
		if err != nil || address.Hostname() == "" || (address.Scheme != "postgres" && address.Scheme != "postgresql") {
			return options, errors.New("AFS_METADATA_URL must be a valid PostgreSQL URL")
		}
	} else if options.databasesFile == "" || options.metadataFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return options, errors.New("cannot locate control-plane configuration; set AFS_METADATA_FILE and AFS_DATABASES_FILE")
		}
		if options.databasesFile == "" {
			options.databasesFile = filepath.Join(home, ".config", "afs-lite", "databases.json")
		}
		if options.metadataFile == "" {
			options.metadataFile = filepath.Join(home, ".config", "afs-lite", "control-plane.sqlite")
		}
	}
	host, _, err := net.SplitHostPort(options.listen)
	if err != nil {
		return options, errors.New("listen must be a host:port address")
	}
	ip := net.ParseIP(host)
	if options.token == "" && !(strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
		return options, errors.New("AFS_CONTROL_PLANE_TOKEN is required when listening outside loopback")
	}
	return options, nil
}

func redisOptions(rawURL string, lookupEnv func(string) (string, bool)) (*redis.Options, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, errors.New("invalid Redis URL")
	}
	if password, ok := lookupEnv("AFS_REDIS_PASSWORD"); ok {
		options.Password = password
	}
	options.DialTimeout = 5 * time.Second
	options.ContextTimeoutEnabled = true
	options.MaxRetries = 1
	return options, nil
}

// Preserve Redis URL options while replacing credentials with the effective
// password, including an explicitly empty AFS_REDIS_PASSWORD override.
func redisConnectionURL(rawURL string, options *redis.Options) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("invalid Redis URL")
	}
	u.User = nil
	if options.Username != "" || options.Password != "" {
		u.User = url.UserPassword(options.Username, options.Password)
	}
	if options.Network != "unix" {
		u.Host = options.Addr
		u.Path = "/" + strconv.Itoa(options.DB)
		u.RawPath = ""
	} else {
		query := u.Query()
		query.Set("db", strconv.Itoa(options.DB))
		u.RawQuery = query.Encode()
	}
	return u.String(), nil
}
