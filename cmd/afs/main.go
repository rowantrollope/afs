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
	"path"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rowantrollope/afs/internal/controlplane"
	"github.com/rowantrollope/afs/internal/version"
	"github.com/rowantrollope/afs/internal/worktree"
	"github.com/rowantrollope/afs/mount/client"
)

const rootUsage = `afs — Persistent agent workspaces backed by Redis.

Usage:
  afs [options] <command>

Commands:
  mount <workspace> <directory>   Sync a workspace with a local folder
  unmount <directory>             Flush pending changes and stop syncing
  status [directory]              Show connection, sync progress, and errors
  ws                             Create and manage workspaces
  fs                             Read and modify workspace files
  cp                             Create and manage checkpoints

Options:
  --redis <url>                   Redis URL; overrides configuration
  --config <file>                 Configuration file
  --json                         Print machine-readable output
  -h, --help                     Show help
  --version                     Show version

Run 'afs <command> --help' for details.
`

var commandUsage = map[string]string{
	"ws": `Usage:
  afs ws create <workspace> [--from <directory>]
  afs ws list
  afs ws info <workspace>
  afs ws fork <source> <new-workspace> [--checkpoint <id-or-name>]
  afs ws delete <workspace> [--yes]
`,
	"fs": `Usage:
  afs fs ls <workspace> [path]
  afs fs cat <workspace> <path>
  afs fs put <workspace> <path> [--from <file>]
  afs fs mkdir <workspace> <path>
  afs fs mv <workspace> <source> <destination>
  afs fs rm <workspace> <path> [--recursive]

Paths are workspace-relative. put reads stdin by default. cat writes exact bytes
and rejects --json. Mutations use the same Redis client as folder synchronization.
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
	"mount": `Usage: afs mount <workspace> <directory> [--foreground]

Start folder synchronization in the background, or stay attached with --foreground.
An unrelated populated directory is rejected. Use ws create --from to import it.
`,
	"unmount": `Usage: afs unmount <directory> [--force]

Flush pending changes and stop synchronization; local files remain intact.
A failed flush leaves synchronization running and returns an error.
--force detaches without flushing. Pending changes may exist only on this machine.
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
	if (args[0] == "ws" || args[0] == "fs" || args[0] == "cp") && len(args) == 1 {
		fmt.Print(usage)
		return nil
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
	case "ws":
		return a.workspace(args[1:])
	case "fs":
		return a.files(args[1:])
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
		return fmt.Errorf("connect to Redis %s: %s", redisDisplay(a.config), redactConnectionError(err, a.config))
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
	f := flag.NewFlagSet("ws", flag.ContinueOnError)
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
		return errors.New(commandUsage["ws"])
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

func workspacePath(s string) (string, error) {
	if strings.ContainsRune(s, '\x00') || strings.Contains(s, "\\") {
		return "", errors.New("path must be workspace-relative with forward slashes")
	}
	if strings.HasPrefix(s, "/") {
		return "", errors.New("path must be workspace-relative")
	}
	for _, segment := range strings.Split(s, "/") {
		if segment == ".." {
			return "", errors.New("path cannot contain ..")
		}
	}
	return path.Join("/", s), nil
}

func (a *app) files(args []string) error {
	f := flag.NewFlagSet("fs", flag.ContinueOnError)
	from := f.String("from", "", "input file")
	recursive := f.Bool("recursive", false, "remove directory tree")
	pos, err := parseCommandFlags(f, args[1:])
	if err != nil {
		return err
	}
	op := args[0]
	if err := allowedFlags(f, map[string][]string{"put": {"from"}, "rm": {"recursive"}}[op]); err != nil {
		return err
	}
	if (op == "ls" && (len(pos) < 1 || len(pos) > 2)) || (op == "mv" && len(pos) != 3) || (op != "ls" && op != "mv" && len(pos) != 2) {
		return errors.New(commandUsage["fs"])
	}
	switch op {
	case "ls", "cat", "put", "mkdir", "mv", "rm":
	default:
		return fmt.Errorf("unknown filesystem command %q", op)
	}
	if op == "cat" && a.options.json {
		return errors.New("fs cat does not support --json; it writes exact file bytes")
	}
	ctx := context.Background()
	if err = a.connect(ctx); err != nil {
		return err
	}
	meta, err := a.service.GetWorkspace(ctx, pos[0])
	if err != nil {
		return err
	}
	generation, err := a.service.WorkspaceGeneration(ctx, meta.ID)
	if err != nil {
		return err
	}
	ctx = client.WithWorkspaceGeneration(ctx, generation)
	fs := client.New(a.rdb, controlplane.WorkspaceFSKey(meta.ID))
	p := "/"
	if len(pos) > 1 {
		p, err = workspacePath(pos[1])
		if err != nil {
			return err
		}
	}
	switch op {
	case "ls":
		entries, e := fs.LsLong(ctx, p)
		if e != nil {
			return e
		}
		return a.output(entries, formatFiles(entries))
	case "cat":
		b, e := fs.Cat(ctx, p)
		if e != nil {
			return e
		}
		_, e = os.Stdout.Write(b)
		return e
	case "put":
		observed, e := fs.Stat(ctx, p)
		if e != nil && !isClientNotFound(e) {
			return e
		}
		ctx = client.WithExpectedStat(ctx, observed)
		var reader io.Reader = os.Stdin
		if *from != "" {
			file, e := os.Open(*from)
			if e != nil {
				return e
			}
			defer file.Close()
			reader = file
		}
		b, e := io.ReadAll(reader)
		if e != nil {
			return e
		}
		if e = fs.Echo(ctx, p, b); e != nil {
			return e
		}
		return a.output(map[string]any{"path": pos[1], "bytes": len(b)}, fmt.Sprintf("Wrote %d bytes to %q in workspace %q.\n", len(b), pos[1], pos[0]))
	case "mkdir":
		err = fs.Mkdir(ctx, p)
	case "mv":
		var dst string
		dst, err = workspacePath(pos[2])
		if err == nil {
			err = fs.Mv(ctx, p, dst)
		}
	case "rm":
		if p == "/" {
			return errors.New("cannot remove the workspace root")
		}
		st, e := fs.Stat(ctx, p)
		if e != nil {
			return e
		}
		if st == nil {
			return errors.New("path does not exist")
		}
		if st.Type == "dir" && !*recursive {
			children, e := fs.Ls(ctx, p)
			if e != nil {
				return e
			}
			if len(children) > 0 {
				return errors.New("directory is not empty; use --recursive")
			}
		}
		if st.Type == "dir" && *recursive {
			err = removeRemoteTree(ctx, fs, p, st)
		} else {
			err = fs.Rm(client.WithExpectedStat(ctx, st), p)
		}
	}
	if err != nil {
		return err
	}
	var message string
	switch op {
	case "mkdir":
		message = fmt.Sprintf("Created directory %q in workspace %q.\n", pos[1], pos[0])
	case "mv":
		message = fmt.Sprintf("Moved %q to %q in workspace %q.\n", pos[1], pos[2], pos[0])
	case "rm":
		message = fmt.Sprintf("Removed %q from workspace %q.\n", pos[1], pos[0])
	}
	return a.output(map[string]any{"operation": op, "path": pos[1]}, message)
}

// Observe the whole tree before removing anything, then use the retained
// conditional remove for each entry. A changed file or newly populated
// directory stops the operation; recursive deletion is not a transaction.
func removeRemoteTree(ctx context.Context, fs client.Client, root string, stat *client.StatResult) error {
	type candidate struct {
		path string
		stat *client.StatResult
	}
	var entries []candidate
	var collect func(string, *client.StatResult) error
	collect = func(p string, st *client.StatResult) error {
		if st == nil {
			return fmt.Errorf("path disappeared during recursive removal: %s", p)
		}
		if st.Type == "dir" {
			names, err := fs.Ls(ctx, p)
			if err != nil {
				return err
			}
			for _, name := range names {
				child := path.Join(p, name)
				observed, err := fs.Stat(ctx, child)
				if err != nil {
					return err
				}
				if err := collect(child, observed); err != nil {
					return err
				}
			}
		}
		entries = append(entries, candidate{p, st})
		return nil
	}
	if err := collect(root, stat); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := fs.Rm(client.WithExpectedStat(ctx, entry.stat), entry.path); err != nil {
			return fmt.Errorf("remove %s: %w", entry.path, err)
		}
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
