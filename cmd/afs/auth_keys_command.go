package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rowantrollope/afs/internal/controlplane"
)

const authKeysCommandUsage = `Usage: afs auth keys <command>

Commands:
  create <name> [--expires 30d|12h|RFC3339|never]  Create an administrator key
  list                                           List keys and usage
  revoke <id>                                    Revoke an API key

Keys grant full access to the control plane and its Redis connections.
Use 'afs auth login --token-stdin' to save a key. New secrets are shown only once.
To replace a key, create and adopt a new key before revoking the old one.
Revocation blocks subsequent API requests; it does not revoke Redis credentials
already issued to mounts or clients. Rotate Redis credentials separately if needed.

Run 'afs auth keys help <command>' for details.
`

var authKeysSubcommandUsage = map[string]string{
	"create": `Usage: afs auth keys create <name> [--expires 30d|12h|RFC3339|never]

Create a named trusted administrator key. The default expiry is 30 days.
Use a positive duration (days, hours, minutes), a future RFC3339 timestamp,
or 'never' to disable expiration. The secret is printed once and is not saved.
Use --json for the key metadata and secret as a JSON object.
`,
	"list": `Usage: afs auth keys list

List key names, IDs, status, creation, expiry, and last usage times.
Secrets are never returned. Use --json for machine-readable metadata.
`,
	"revoke": `Usage: afs auth keys revoke <id>

Revoke a key by its ID. Future API requests with this key will be rejected.
Previously issued Redis credentials and existing mounts are unaffected.
`,
}

func authKeysCommand(opts cliOptions, args []string) error {
	if len(args) == 0 || (len(args) == 1 && isHelpArg(args[0])) {
		fmt.Print(authKeysCommandUsage)
		return nil
	}
	if isHelpArg(args[0]) {
		if usage, ok := authKeysSubcommandUsage[args[1]]; ok && len(args) == 2 {
			fmt.Print(usage)
			return nil
		}
		return errors.New("usage: afs auth keys help [create|list|revoke]")
	}
	usage, ok := authKeysSubcommandUsage[args[0]]
	if !ok {
		return errors.New("unknown API key command; run afs auth keys --help")
	}
	for _, arg := range args[1:] {
		if arg == "--" {
			break
		}
		if arg == "--help" || arg == "-h" {
			fmt.Print(usage)
			return nil
		}
	}
	if opts.redisURL != "" {
		return errors.New("auth commands do not accept --redis; use auth login to select a control plane")
	}
	flags := flag.NewFlagSet("auth keys "+args[0], flag.ContinueOnError)
	expires := "30d"
	if args[0] == "create" {
		flags.StringVar(&expires, "expires", expires, "expiry duration, RFC3339 timestamp, or never")
	}
	pos, err := parseCommandFlags(flags, args[1:])
	if err != nil {
		return err
	}
	expectedArgs := 1
	if args[0] == "list" {
		expectedArgs = 0
	}
	if len(pos) != expectedArgs {
		return errors.New(usage)
	}
	expiresAt := ""
	if args[0] == "create" {
		pos[0] = strings.TrimSpace(pos[0])
		if pos[0] == "" {
			return errors.New("API key name must not be empty")
		}
		expiresAt, err = apiKeyExpiry(expires, time.Now())
		if err != nil {
			return err
		}
	}
	if args[0] == "revoke" && strings.TrimSpace(pos[0]) == "" {
		return errors.New("API key ID must not be empty")
	}
	remote, err := authKeysClient(opts)
	if err != nil {
		return err
	}
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a := app{options: opts}
	switch args[0] {
	case "create":
		result, err := remote.CreateAPIKey(ctx, pos[0], expiresAt)
		if err != nil {
			return err
		}
		return a.output(result, formatAPIKey(result.Key)+
			"\nSave this administrator key now; it will not be shown again.\n"+textCell(result.Token)+"\n")
	case "list":
		result, err := remote.ListAPIKeys(ctx)
		if err != nil {
			return err
		}
		return a.output(result, formatAPIKeyList(result))
	case "revoke":
		key, err := remote.RevokeAPIKey(ctx, pos[0])
		if err != nil {
			return err
		}
		return a.output(map[string]any{"key": key}, fmt.Sprintf("Revoked API key %s (%s). Existing Redis access is unaffected.\n", textCell(key.ID), textCell(key.Name)))
	}
	return nil
}

func authKeysClient(opts cliOptions) (*controlplane.CLIClient, error) {
	file, err := configFilePath(opts.configPath)
	if err != nil {
		return nil, err
	}
	stored, err := storedAuthSettings(file)
	if err != nil {
		return nil, err
	}
	settings, _ := effectiveAuthSettings(stored)
	if settings.URL == "" {
		return nil, errors.New("API keys require a control plane; run afs auth login --url <URL>")
	}
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	return controlplane.NewCLIClient(settings.URL, settings.Token)
}

func apiKeyExpiry(value string, now time.Time) (string, error) {
	if value == "never" {
		return "", nil
	}
	invalid := errors.New("--expires must be a positive duration, a future RFC3339 timestamp, or never")
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		if !at.After(now) {
			return "", invalid
		}
		return at.UTC().Format(time.RFC3339Nano), nil
	}
	var duration time.Duration
	var err error
	if strings.HasSuffix(value, "d") {
		var days int64
		days, err = strconv.ParseInt(strings.TrimSuffix(value, "d"), 10, 64)
		const maxDays = int64((1<<63 - 1) / (24 * time.Hour))
		if err != nil || days <= 0 || days > maxDays {
			return "", invalid
		}
		duration = time.Duration(days) * 24 * time.Hour
	} else {
		duration, err = time.ParseDuration(value)
	}
	if err != nil || duration <= 0 {
		return "", invalid
	}
	return now.Add(duration).UTC().Format(time.RFC3339Nano), nil
}

func formatAPIKey(key controlplane.APIKey) string {
	expires := key.ExpiresAt
	if expires == "" {
		expires = "never"
	}
	return textTable(nil, [][]string{
		{"Name:", key.Name}, {"ID:", key.ID}, {"Status:", key.Status},
		{"Created:", key.CreatedAt}, {"Expires:", expires}, {"Last used:", key.LastUsedAt},
	})
}

func formatAPIKeyList(result controlplane.APIKeyList) string {
	if !result.Enabled {
		return "API key management is disabled. Configure the server's AFS_CONTROL_PLANE_TOKEN to enable it.\n"
	}
	if len(result.Keys) == 0 {
		return "No API keys.\n"
	}
	rows := make([][]string, 0, len(result.Keys))
	for _, key := range result.Keys {
		expires := key.ExpiresAt
		if expires == "" {
			expires = "never"
		}
		rows = append(rows, []string{key.Name, key.ID, key.Status, key.CreatedAt, expires, key.LastUsedAt})
	}
	return textTable([]string{"NAME", "ID", "STATUS", "CREATED", "EXPIRES", "LAST USED"}, rows)
}
