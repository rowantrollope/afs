package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/managedclient"
)

const defaultControlPlaneURL = "http://127.0.0.1:8091"

const authCommandUsage = `Usage: afs auth <command>

Commands:
  login    Connect to a self-managed control plane
  status   Show effective connection settings without contacting the server
  logout   Clear the saved control-plane connection
  keys     Create, list, and revoke trusted administrator API keys

Run 'afs auth help <command>' for details.
`

var authSubcommandUsage = map[string]string{
	"login": `Usage: afs auth login [--url <URL>] [--token-stdin] [--self-hosted]

Verify control-plane authentication, then save its URL and optional team token or API key.
The default is the configured or environment URL, or http://127.0.0.1:8091.
--control-plane-url is an alias for --url; --self-hosted is accepted for compatibility.
Use --token-stdin to read one token line from stdin (maximum 16384 bytes).
Environment tokens are used at runtime and are not saved implicitly.
Redis credentials stay with the server and are obtained when mounting.
Login works before a Redis database is configured or while Redis is unavailable.
`,
	"status": `Usage: afs auth status

Show effective managed or standalone settings, token presence and environment
overrides. This is an offline check; it does not verify server connectivity.
`,
	"logout": `Usage: afs auth logout

Clear the saved control-plane URL and token. Redis and other settings remain.
Environment overrides and existing mounts are unaffected. No server is contacted.
`,
	"keys": authKeysCommandUsage,
}

func authCommand(opts cliOptions, args []string) error {
	if len(args) == 0 {
		fmt.Print(authCommandUsage)
		return nil
	}
	if isHelpArg(args[0]) {
		if len(args) == 1 {
			fmt.Print(authCommandUsage)
			return nil
		}
		if usage, ok := authSubcommandUsage[args[1]]; ok && len(args) == 2 {
			fmt.Print(usage)
			return nil
		}
		return errors.New("usage: afs auth help [login|status|logout|keys]")
	}
	if args[0] == "keys" {
		return authKeysCommand(opts, args[1:])
	}
	usage, ok := authSubcommandUsage[args[0]]
	if !ok {
		return fmt.Errorf("unknown auth command %q; run afs auth --help", args[0])
	}
	for _, arg := range args[1:] {
		if arg == "--help" || arg == "-h" {
			fmt.Print(usage)
			return nil
		}
	}
	if opts.redisURL != "" {
		return errors.New("auth commands do not accept --redis; use --url with auth login to select a control plane")
	}
	if args[0] == "login" {
		return authLogin(opts, args[1:])
	}
	if len(args) != 1 {
		return errors.New(usage)
	}
	if args[0] == "status" {
		return authStatus(opts)
	}
	return authLogout(opts)
}

func normalizedControlPlaneURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func storedAuthSettings(file string) (managedclient.Settings, error) {
	document, err := readConfigDocument(file)
	if err != nil {
		return managedclient.Settings{}, err
	}
	var settings managedclient.Settings
	if raw, ok := document["controlPlane"]; ok {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return settings, errors.New("invalid controlPlane configuration")
		}
	}
	return settings, nil
}

type authStatusResult struct {
	Mode                string `json:"mode"`
	URL                 string `json:"url,omitempty"`
	URLSource           string `json:"url_source"`
	TokenPresent        bool   `json:"token_present"`
	TokenSource         string `json:"token_source"`
	EnvironmentOverride bool   `json:"environment_override"`
	Connectivity        string `json:"connectivity"`
}

func effectiveAuthSettings(stored managedclient.Settings) (managedclient.Settings, authStatusResult) {
	settings := stored
	settings.URL = normalizedControlPlaneURL(settings.URL)
	result := authStatusResult{Mode: "standalone", URLSource: "none", TokenSource: "none", Connectivity: "not_checked"}
	if settings.URL != "" {
		result.URLSource = "config"
	}
	if value, present := os.LookupEnv("AFS_CONTROL_PLANE_URL"); present {
		settings.URL = normalizedControlPlaneURL(value)
		result.URLSource = "environment"
		result.EnvironmentOverride = true
		if settings.URL != normalizedControlPlaneURL(stored.URL) {
			settings.Token = ""
		}
	}
	if settings.Token != "" {
		result.TokenSource = "config"
	}
	if value, present := os.LookupEnv("AFS_CONTROL_PLANE_TOKEN"); present {
		settings.Token = value
		result.TokenSource = "environment"
		result.EnvironmentOverride = true
	}
	result.URL = settings.URL
	result.TokenPresent = settings.Token != ""
	if settings.URL != "" {
		result.Mode = "managed"
	}
	return settings, result
}

func saveAuthSettings(file string, settings managedclient.Settings) error {
	endpoint, _ := json.Marshal(settings.URL)
	token, _ := json.Marshal(settings.Token)
	return updateConfigDocument(file, func(document map[string]json.RawMessage) error {
		if err := setConfigDocumentValue(document, "controlPlane.url", endpoint); err != nil {
			return err
		}
		return setConfigDocumentValue(document, "controlPlane.token", token)
	})
}

func authLogin(opts cliOptions, args []string) error {
	flags := flag.NewFlagSet("auth login", flag.ContinueOnError)
	var endpoint string
	flags.StringVar(&endpoint, "url", "", "control-plane URL")
	flags.StringVar(&endpoint, "control-plane-url", "", "control-plane URL")
	flags.Bool("self-hosted", false, "self-managed compatibility flag")
	tokenStdin := flags.Bool("token-stdin", false, "read team token or API key from stdin")
	pos, err := parseCommandFlags(flags, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return errors.New(authSubcommandUsage["login"])
	}
	explicitURL := false
	flags.Visit(func(f *flag.Flag) { explicitURL = explicitURL || f.Name == "url" || f.Name == "control-plane-url" })
	file, err := configFilePath(opts.configPath)
	if err != nil {
		return err
	}
	stored, err := storedAuthSettings(file)
	if err != nil {
		return err
	}
	settings, _ := effectiveAuthSettings(stored)
	if explicitURL {
		endpoint = normalizedControlPlaneURL(endpoint)
		if endpoint == "" {
			return errors.New("auth login URL must not be empty")
		}
		if envURL, present := os.LookupEnv("AFS_CONTROL_PLANE_URL"); present && endpoint != normalizedControlPlaneURL(envURL) {
			return errors.New("--url differs from AFS_CONTROL_PLANE_URL; unset AFS_CONTROL_PLANE_URL before changing the saved endpoint")
		}
		settings.URL = endpoint
	}
	if settings.URL == "" {
		if _, present := os.LookupEnv("AFS_CONTROL_PLANE_URL"); present {
			return errors.New("AFS_CONTROL_PLANE_URL disables the saved connection; unset it before logging in")
		}
		settings.URL = defaultControlPlaneURL
	}
	savedToken := ""
	if settings.URL == normalizedControlPlaneURL(stored.URL) {
		savedToken = stored.Token
	}
	settings.Token = savedToken
	if envToken, present := os.LookupEnv("AFS_CONTROL_PLANE_TOKEN"); present {
		settings.Token = envToken
	}
	if *tokenStdin {
		token, err := readControlPlaneToken(os.Stdin)
		if err != nil {
			return err
		}
		if envToken, present := os.LookupEnv("AFS_CONTROL_PLANE_TOKEN"); present && token != envToken {
			return errors.New("stdin token differs from AFS_CONTROL_PLANE_TOKEN; unset AFS_CONTROL_PLANE_TOKEN before saving a different token")
		}
		settings.Token, savedToken = token, token
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	remote, err := controlplane.NewCLIClient(settings.URL, settings.Token)
	if err != nil {
		return err
	}
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := remote.VerifyAuthentication(ctx); err != nil {
		return err
	}
	if err := saveAuthSettings(file, managedclient.Settings{URL: settings.URL, Token: savedToken}); err != nil {
		return err
	}
	a := app{options: opts}
	return a.output(map[string]any{"logged_in": true, "url": settings.URL, "config_path": file, "token_saved": savedToken != ""},
		fmt.Sprintf("Connected to %s.\nSaved control-plane settings in %s.\n", settings.URL, file))
}

func authStatus(opts cliOptions) error {
	file, err := configFilePath(opts.configPath)
	if err != nil {
		return err
	}
	stored, err := storedAuthSettings(file)
	if err != nil {
		return err
	}
	settings, result := effectiveAuthSettings(stored)
	if err := settings.Validate(); err != nil {
		return err
	}
	rows := [][]string{{"Mode:", result.Mode}, {"Control plane:", result.URL}, {"URL source:", result.URLSource}, {"Token:", "not set"}, {"Connectivity:", "not checked (offline)"}}
	if result.TokenPresent {
		rows[3][1] = "set (" + result.TokenSource + ")"
	}
	text := textTable(nil, rows)
	if result.EnvironmentOverride {
		text += "Environment overrides are active.\n"
	}
	a := app{options: opts}
	return a.output(result, text)
}

func authLogout(opts cliOptions) error {
	file, err := configFilePath(opts.configPath)
	if err != nil {
		return err
	}
	if err := saveAuthSettings(file, managedclient.Settings{}); err != nil {
		return err
	}
	_, urlOverride := os.LookupEnv("AFS_CONTROL_PLANE_URL")
	_, tokenOverride := os.LookupEnv("AFS_CONTROL_PLANE_TOKEN")
	environment := urlOverride || tokenOverride
	text := "Cleared saved control-plane URL and token. Existing mounts are unaffected.\n"
	if environment {
		text += "Environment overrides remain active; unset AFS_CONTROL_PLANE_URL and AFS_CONTROL_PLANE_TOKEN to clear them.\n"
	}
	a := app{options: opts}
	return a.output(map[string]any{"logged_out": true, "config_path": file, "environment_override": environment, "existing_mounts_unchanged": true}, text)
}
