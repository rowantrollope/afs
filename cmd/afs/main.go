package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/logging"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/version"
	"github.com/rowantrollope/afs/internal/worktree"
)

const rootUsage = `afs — Persistent agent workspaces backed by Redis.

Usage:
  afs [options] <command>

Commands:
  mount <workspace> <directory>  Connect a workspace using sync, FUSE, or NFS
  unmount <directory>            Flush pending changes and disconnect
  status [directory]             Show connection, sync progress, and errors
  create <workspace>             Create a workspace or import a directory
  list                           List workspaces
  info <workspace>               Show workspace details
  fork <source> <new-workspace>  Fork a workspace from a checkpoint
  delete <workspace>             Delete a workspace
  cp                             Create and manage checkpoints
  config set <key> <value>       Save a configuration setting

Options:
  --redis <url>                  Redis URL; overrides configuration
  --config <file>                Configuration file
  --json                         Print machine-readable output
  -h, --help                     Show help
  --version                      Show version

Run 'afs <command> --help' for details.
`

var commandUsage = map[string]string{
	"config": `Usage: afs [--config <file>] config set <key> <value>

Settings (defaults):
  redis                       redis://localhost:6379/0
  sync.fileSizeCapMB           2048 (0 uses the default)
  sync.watcherQueueCapacity    1024 (0 uses the default; maximum 1048576)

Saves to ~/.config/afs-lite/config.json unless --config selects another file.
Creates a missing file with all defaults. Preserves other settings and never
connects to Redis. Existing config files must be regular files, not symlinks.
Changes apply to subsequent commands and newly started mounts.
`,
	"create": `Usage: afs create <workspace> [--from <directory>]

Create an empty workspace, or import an existing local directory with --from.
`,
	"list": "Usage: afs list\n\nList workspaces in the selected Redis database.\n",
	"info": "Usage: afs info <workspace>\n\nShow workspace details and checkpoint references.\n",
	"fork": `Usage: afs fork <source> <new-workspace> [--checkpoint <id-or-name>]

Create an independent workspace from a checkpoint. Without --checkpoint, use
its most recently created checkpoint; uncheckpointed changes are excluded.
`,
	"delete": `Usage: afs delete <workspace> [--yes]

Delete a workspace. Local mounts must be unmounted first; confirmation is required.
`,
	"cp": `Usage:
  afs cp create <workspace> [--name <name>]
  afs cp list <workspace>
  afs cp show <workspace> <id-or-name>
  afs cp restore <workspace> <id-or-name> [--yes]
  afs cp delete <workspace> <id-or-name> [--yes]

Create flushes this machine's registered mounts first; a failed flush fails the
command. It captures published remote state, not another machine's pending writes.
Applications must pause writes for an application-consistent snapshot.
Restore requires local mounts to be unmounted and creates a safety checkpoint.
`,
	"mount": `Usage: afs mount <workspace> <directory> [--foreground] [--backend sync|fuse|nfs]

Start folder synchronization in the background, or stay attached with --foreground.
An unrelated populated directory is rejected. Import it with:
  afs create <new-workspace> --from <directory>

--backend fuse or nfs uses the optional afsmount helper to expose the workspace
as a native filesystem. Native mountpoints must be empty. Files live in Redis;
unmounting reveals the original local directory. Folder sync remains the default.
`,
	"unmount": `Usage: afs unmount <directory> [--force]

Flush pending changes and stop synchronization; local files remain intact.
A failed flush leaves synchronization running and returns an error.
--force detaches without flushing. Pending changes may exist only on this machine.
For native mounts, unmount detaches the filesystem after flushing kernel writes;
a failed normal unmount leaves the helper serving. --force requests forced detach.
`,
	"status": "Usage: afs status [directory]\n\nShow locally registered mounts, connection state, pending work, and errors.\n",
}

type cliOptions struct {
	redisURL, configPath string
	json                 bool
}
type app struct {
	config  config
	options cliOptions
	rdb     *redis.Client
	service *controlplane.Service
}

func main() {
	// AFS reports operation errors itself; suppress duplicate Redis diagnostics.
	logging.Disable()
	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "afs:", err)
		os.Exit(1)
	}
}

func runCLI(args []string) error {
	if len(args) > 0 && args[0] == "_sync-daemon" {
		return runSyncDaemon()
	}
	opts, args, err := parseGlobalOptions(args)
	if err != nil {
		return err
	}
	if len(args) == 0 || isHelpArg(args[0]) {
		fmt.Print(rootUsage)
		return nil
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Println("afs " + version.String())
		return nil
	}
	usage, known := commandUsage[args[0]]
	if !known {
		return fmt.Errorf("unknown command %q; run afs --help", args[0])
	}
	for _, a := range args[1:] {
		if a == "--" {
			break
		}
		if a == "--help" || a == "-h" {
			fmt.Print(usage)
			return nil
		}
	}
	if args[0] == "cp" && len(args) == 1 {
		fmt.Print(usage)
		return nil
	}
	if args[0] == "config" {
		return configCommand(opts, args[1:])
	}
	cfg, err := readConfig(opts.configPath, opts.redisURL)
	if err != nil {
		return err
	}
	a := &app{config: cfg, options: opts}
	defer func() {
		if a.rdb != nil {
			_ = a.rdb.Close()
		}
	}()
	switch args[0] {
	case "create", "list", "info", "fork", "delete":
		return a.workspace(args)
	case "cp":
		return a.checkpoints(args[1:])
	case "mount":
		return a.mount(args[1:])
	case "unmount":
		return a.unmount(args[1:])
	case "status":
		return a.status(args[1:])
	}
	return nil
}

func parseGlobalOptions(args []string) (cliOptions, []string, error) {
	var o cliOptions
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--redis", "--config":
			if !hasValue {
				i++
				if i >= len(args) {
					return o, nil, fmt.Errorf("%s needs a value", name)
				}
				value = args[i]
			}
			if name == "--redis" {
				o.redisURL = value
			} else {
				o.configPath = value
			}
		case "--json":
			if hasValue {
				return o, nil, errors.New("--json does not take a value")
			}
			o.json = true
		default:
			rest = append(rest, arg)
		}
	}
	return o, rest, nil
}

func isHelpArg(s string) bool { return s == "--help" || s == "-h" || s == "help" }

// parseCommandFlags allows the documented trailing flags with Go's small flag package.
func parseCommandFlags(f *flag.FlagSet, args []string) ([]string, error) {
	f.SetOutput(io.Discard)
	var positional []string
	for i := 0; i < len(args); i++ {
		s := args[i]
		if s == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(s, "-") || s == "-" {
			positional = append(positional, s)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(s, "-"), "=")
		def := f.Lookup(name)
		if def == nil {
			return nil, fmt.Errorf("unknown option --%s", name)
		}
		if !hasValue {
			if b, ok := def.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				value = "true"
			} else {
				i++
				if i >= len(args) {
					return nil, fmt.Errorf("--%s needs a value", name)
				}
				value = args[i]
			}
		}
		if err := f.Set(name, value); err != nil {
			return nil, err
		}
	}
	return positional, nil
}

func allowedFlags(f *flag.FlagSet, names []string) error {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		allowed[name] = true
	}
	var err error
	f.Visit(func(option *flag.Flag) {
		if !allowed[option.Name] {
			err = fmt.Errorf("option --%s is not valid for this command", option.Name)
		}
	})
	return err
}

func (a *app) connect(ctx context.Context) error {
	if a.rdb != nil {
		return nil
	}
	rdb := redis.NewClient(buildRedisOptions(a.config, 8))
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return redisConnectionError(a.config)
	}
	a.rdb = rdb
	a.service = controlplane.NewService(controlplane.NewStore(rdb))
	return nil
}

// Both output modes share the same result. Human presentation is explicit at
// each call site; JSON keeps the existing machine-readable schema.
func (a *app) output(v any, text string) error {
	if a.options.json {
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	_, err := io.WriteString(os.Stdout, text)
	return err
}

func confirmDeletion(action string, yes bool) error {
	if yes {
		return nil
	}
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("%s requires confirmation; pass --yes", action)
	}
	fmt.Fprintf(os.Stderr, "%s? [y/N] ", action)
	response, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(response)) != "y" {
		return errors.New("cancelled")
	}
	return nil
}

func (a *app) workspace(args []string) error {
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	from := f.String("from", "", "import directory")
	checkpoint := f.String("checkpoint", "", "fork checkpoint")
	yes := f.Bool("yes", false, "confirm deletion")
	pos, err := parseCommandFlags(f, args[1:])
	if err != nil {
		return err
	}
	op := args[0]
	if err := allowedFlags(f, map[string][]string{"create": {"from"}, "fork": {"checkpoint"}, "delete": {"yes"}}[op]); err != nil {
		return err
	}
	n := len(pos)
	if (op == "list" && n != 0) || (op == "fork" && n != 2) || (op != "list" && op != "fork" && n != 1) {
		return errors.New(commandUsage[op])
	}
	switch op {
	case "create", "list", "info", "fork", "delete":
	default:
		return fmt.Errorf("unknown workspace command %q", op)
	}
	ctx := context.Background()
	if err = a.connect(ctx); err != nil {
		return err
	}
	switch op {
	case "create":
		var meta controlplane.WorkspaceMeta
		if *from == "" {
			meta, err = a.service.CreateWorkspace(ctx, pos[0])
		} else {
			root, e := expandPath(*from)
			if e != nil {
				return e
			}
			ignore, e := loadMigrationIgnore(root)
			if e != nil {
				return e
			}

			meta, err = a.service.CreateWorkspaceStreaming(ctx, pos[0], func(id string, writer *controlplane.BlobWriter) (controlplane.Manifest, error) {
				build := worktree.BuildManifestOptions{Sink: importSink{ctx: ctx, writer: writer}}
				if ignore != nil {
					build.Ignore = ignore.matches
				}
				m, _, _, e := worktree.BuildManifestFromDirectory(root, id, "", build)
				return m, e
			})
		}
		if err != nil {
			return err
		}
		return a.output(meta, fmt.Sprintf("Created workspace %q.\n", meta.Name))
	case "list":
		v, e := a.service.ListWorkspaces(ctx)
		if e != nil {
			return e
		}
		return a.output(v, formatWorkspaces(v))
	case "info":
		v, e := a.service.GetWorkspace(ctx, pos[0])
		if e != nil {
			return e
		}
		return a.output(v, formatWorkspace(v))
	case "fork":
		if e := a.service.ForkWorkspace(ctx, pos[0], pos[1], *checkpoint); e != nil {
			return e
		}
		return a.output(map[string]any{"workspace": pos[1], "forked_from": pos[0]}, fmt.Sprintf("Forked workspace %q from %q.\n", pos[1], pos[0]))
	case "delete":
		if e := a.requireUnmounted(ctx, pos[0]); e != nil {
			return e
		}
		if e := confirmDeletion("Delete workspace "+pos[0], *yes); e != nil {
			return e
		}
		if e := a.service.DeleteWorkspace(ctx, pos[0]); e != nil {
			return e
		}
		return a.output(map[string]any{"deleted": pos[0]}, fmt.Sprintf("Deleted workspace %q.\n", pos[0]))
	}
	return nil
}

func (a *app) checkpoints(args []string) error {
	f := flag.NewFlagSet("cp", flag.ContinueOnError)
	name := f.String("name", "", "checkpoint name")
	yes := f.Bool("yes", false, "confirm destructive operation")
	pos, err := parseCommandFlags(f, args[1:])
	if err != nil {
		return err
	}
	op := args[0]
	if err := allowedFlags(f, map[string][]string{"create": {"name"}, "restore": {"yes"}, "delete": {"yes"}}[op]); err != nil {
		return err
	}
	want := 2
	if op == "create" || op == "list" {
		want = 1
	}
	if len(pos) != want {
		return errors.New(commandUsage["cp"])
	}
	ctx := context.Background()
	if err = a.connect(ctx); err != nil {
		return err
	}
	switch op {
	case "create":
		if err = a.flushLocalMounts(ctx, pos[0]); err != nil {
			return fmt.Errorf("checkpoint aborted: %w", err)
		}
		v, e := a.service.SaveCheckpointFromLive(ctx, pos[0], *name)
		if e != nil {
			return e
		}
		return a.output(v, fmt.Sprintf("Created checkpoint %q (%s) in workspace %q.\n", v.Name, v.ID, pos[0]))
	case "list":
		v, e := a.service.ListCheckpoints(ctx, pos[0])
		if e != nil {
			return e
		}
		return a.output(v, formatCheckpoints(v))
	case "show":
		meta, m, e := a.service.GetCheckpoint(ctx, pos[0], pos[1])
		if e != nil {
			return e
		}
		return a.output(map[string]any{"checkpoint": meta, "manifest": m}, formatCheckpoint(pos[0], meta, m))
	case "restore":
		if err = a.requireUnmounted(ctx, pos[0]); err != nil {
			return err
		}
		if err = confirmDeletion("Restore checkpoint "+pos[1]+" in "+pos[0], *yes); err != nil {
			return err
		}
		v, e := a.service.RestoreCheckpoint(ctx, pos[0], pos[1])
		if e != nil {
			return e
		}
		return a.output(v, formatRestore(v))
	case "delete":
		if err = confirmDeletion("Delete checkpoint "+pos[1]+" in "+pos[0], *yes); err != nil {
			return err
		}
		if err = a.service.DeleteCheckpoint(ctx, pos[0], pos[1]); err != nil {
			return err
		}
		return a.output(map[string]any{"deleted": pos[1], "workspace": pos[0]}, fmt.Sprintf("Deleted checkpoint %q from workspace %q.\n", pos[1], pos[0]))
	default:
		return fmt.Errorf("unknown checkpoint command %q", op)
	}
}
