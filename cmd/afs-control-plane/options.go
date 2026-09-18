package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/redis/go-redis/v9"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type serverOptions struct {
	listen, redisURL, token string
	origins                 stringList
	showVersion             bool
}
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func parseOptions(args []string, getenv func(string) string, output io.Writer) (serverOptions, error) {
	options := serverOptions{listen: "127.0.0.1:8091", redisURL: "redis://localhost:6379/0", token: strings.TrimSpace(getenv("AFS_CONTROL_PLANE_TOKEN"))}
	if value := getenv("AFS_CONTROL_PLANE_LISTEN"); value != "" {
		options.listen = value
	}
	if value := getenv("AFS_REDIS_URL"); value != "" {
		options.redisURL = value
	}
	flags := flag.NewFlagSet("afs-control-plane", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&options.listen, "listen", options.listen, "HTTP listen address (AFS_CONTROL_PLANE_LISTEN)")
	flags.StringVar(&options.redisURL, "redis", options.redisURL, "Redis URL (AFS_REDIS_URL); default redis://localhost:6379/0; password override: AFS_REDIS_PASSWORD")
	// flag defaults appear in help; the selected URL may contain a password.
	flags.Lookup("redis").DefValue = ""
	flags.Var(&options.origins, "allow-origin", "Allowed browser origin; repeat for multiple origins")
	flags.BoolVar(&options.showVersion, "version", false, "Print version")
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: afs-control-plane [options]\n\nOptional AFS management API and UI. Files continue to flow directly to Redis.\nSet AFS_CONTROL_PLANE_TOKEN to require a bearer token; required off loopback.")
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
