# AFS

Persistent agent workspaces backed by Redis. Mount the same workspace on two
machines and edit ordinary local files; changes synchronize in both directions.
One workspace owns one file tree, its checkpoints and optional per-file history.

AFS is a slim Go derivative of [redis/agent-filesystem](https://github.com/redis/agent-filesystem),
retaining its folder sync, inode storage, manifest checkpoints and recovery code,
with optional FUSE and NFS mounting through a separate helper.
See the [simplification report](docs/simplification.md) for provenance and changes.

## Build

Requires Go 1.22.2 or newer and a Redis server. Supported local platforms are
macOS and Linux; acceptance runs cover Redis 7.0.15 and 8.6.2. The default folder
sync needs no FUSE, NFS, control-plane server or agent plugin.

```sh
git clone https://github.com/rowantrollope/afs.git
cd afs
go build -o bin/afs ./cmd/afs
./bin/afs --help
```

## Install

From the checkout, build and install the command for your user:

```sh
make install
afs --help
```

This creates `~/.local/bin/afs` as a symlink to this checkout's `bin/afs`, without
sudo. It also creates `~/.config/afs-lite/config.json` from
[`config.example.json`](config.example.json) if no config exists, preserving any
existing configuration. Rebuilding with `make build` updates the command automatically. Keep the
checkout in place; rerun `make install` after moving it, removing the old link first.

For a custom destination, run `make install INSTALL_DIR=/your/bin`. Repeating
`make install` is safe; an existing regular AFS executable is upgraded to the
checkout symlink. Other files, directories and links to a different location
are preserved, and installation stops with an error.

If `~/.local/bin` is not on your PATH, add this to `~/.zshrc` (zsh) or
`~/.bashrc` (bash), then open a new terminal:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

To uninstall the default link, run `rm ~/.local/bin/afs`. You can also continue
using `./bin/afs` directly. The original `agent-filesystem` installation is unchanged.

## Configure

Save your default Redis connection from the CLI (no Redis connection is needed):

```sh
afs config set redis 'redis://localhost:6379/0'
afs list
```

`make install` creates `~/.config/afs-lite/config.json` if it is missing.
`config set` also creates a missing file with all defaults. The included
[`config.example.json`](config.example.json) shows every supported setting:

```json
{
  "redis": "redis://localhost:6379/0",
  "sync": {
    "fileSizeCapMB": 2048,
    "watcherQueueCapacity": 1024
  }
}
```

Change sync settings using their dotted names:

```sh
afs config set sync.fileSizeCapMB 512
afs config set sync.watcherQueueCapacity 2048
afs --config ./afs.json config set redis 'rediss://default:PASSWORD@HOST:PORT/0'
```

`fileSizeCapMB` is the per-file sync limit in MB. `watcherQueueCapacity` is the
number of buffered watcher events (maximum 1048576). Zero selects the built-in
default for either setting. Changes apply to subsequent commands and newly
started mounts; restart an existing mount to apply new settings.

`config set` preserves other JSON settings, validates the result, and saves it
atomically with permissions `0600`. It refuses malformed files or symlink config
paths and never prints the supplied value. Use `--config <file>` to select an
alternate file for either configuration updates or ordinary commands.

For a one-command override:

```sh
afs --redis 'redis://localhost:6379/0' list
```

Connection precedence is `--redis`, then nonempty `AFS_REDIS_URL`, then the
config file, then `redis://localhost:6379/0`. An empty `AFS_REDIS_URL` is ignored.
Neither environment overrides nor `--redis` change the saved configuration.
URLs support username/password, a database number, and TLS via `rediss://`.
Passwords in saved URLs remain supported. Set `AFS_REDIS_PASSWORD` to override
the password in either the saved URL or `--redis`, without changing the file.
An explicitly empty variable overrides with an empty password; unset it to use
the URL password again. The username and endpoint still come from the URL.
The override is read when connecting and inherited by newly started background
sync/native mounts; changing your shell environment does not update running mounts.
AFS never writes `AFS_REDIS_PASSWORD` into config or bootstrap files. Use a
password-free `AFS_REDIS_URL` with this separate variable to keep the password
out of mount bootstrap files as well.

For example, in zsh, prompt without echoing the password or putting it in history:

```zsh
export AFS_REDIS_URL='rediss://default@HOST:PORT/0'
read -rs 'AFS_REDIS_PASSWORD?Redis password: '; printf '\n'
export AFS_REDIS_PASSWORD
afs list
unset AFS_REDIS_PASSWORD
```

Environment variables are not a secret vault; privileged processes and diagnostic
tools may expose them. AFS cannot set variables in its parent shell. Saving a URL
with a password prints a reminder about the override (human output only); this
does not remove the saved password or any existing shell-history entry.
Help, version, and config commands work without Redis.

Redis keys use `afs-lite:`; local state uses `~/.afs-lite`. These do not reuse the
original project's namespace or registry. `AFS_STATE_DIR` selects another local
registry/state directory, useful for isolated client tests. Each mount has its own
saved baseline and private daemon log beneath that directory.

## Two writable clients

Both machines must reach the **same Redis database**. First create the workspace:

```sh
afs create shared
# Or import a directory using the original parallel import machinery:
afs create project --from ./existing-project
```

On machine A:

```sh
afs mount shared ~/agent-a
echo 'from A' > ~/agent-a/message.txt
```

On machine B:

```sh
afs mount shared ~/agent-b
cat ~/agent-b/message.txt
echo 'from B' > ~/agent-b/reply.txt
```

`reply.txt` appears on machine A automatically. `mount` starts background folder
synchronization and returns after initial synchronization. Wait for mount to
finish before starting applications in that directory. Use `--foreground` for
supervisors or debugging. An unrelated populated directory is rejected.

```sh
afs status
afs status ~/agent-a
afs unmount ~/agent-a
```

Unmount joins pending work, verifies published bytes, and stops synchronization.
It leaves local files intact. A failed flush returns an error and keeps the daemon
running. `unmount --force` detaches without a flush; pending content may exist only
in the local directory. Ctrl-C in foreground mode also attempts a flush.

For a client that only receives workspace changes:

```sh
afs mount shared ~/observer --readonly
```

A read-only sync mount receives remote updates and never publishes local edits,
deletions or permission changes. Downloaded regular files have their write bits
removed while retaining read and execute bits. The local directory remains
usable by the synchronizer; this is not a host permission or Redis authorization
boundary. Local-only files stay local, and recovery preserves divergent file
contents as local conflict copies. Normal unmount stops the reader without a
save receipt; checkpoints skip its local contents. Remount with the same
`--readonly` setting, or use a new directory to change access mode.

## Sync status and verified completion

Synchronization runs automatically. Inspect folder-sync mounts without forcing
a flush:

```sh
afs sync status
afs sync status ~/agent-a
```

For a script that must confirm publication before continuing, pause application
writes and other writers to the workspace, then use:

```sh
afs sync --wait ~/agent-a
afs sync --wait shared --timeout 10m --json
```

`--wait` drains background work, discovers pending changes even if watcher events
were missed, publishes them and reads back the included tree to verify file
bytes, types, permissions and symlink targets. Ignore rules still apply. It
resumes synchronization and creates no checkpoint. The JSON `receipt` contains
entry/file counts, byte count, tree SHA-256 and completion time. Failures include
`success: false`, `verified: false` and an error, with a nonzero exit status.
Timeouts and failures can leave partially published changes; they never confirm
completion. The default timeout is two minutes (`1ms` through `24h` supported).

The target is an exact mount directory or an unambiguous locally mounted workspace
name/ID. A stopped, read-only or native mount cannot publish through `--wait`.
`sync status` lists folder-sync mounts; `afs status` remains the overview for all
mount backends. Empty queues are only an observation, not verified completion.
A receipt confirms Redis visibility, not disk persistence or delivery to other
mounted clients. Pause writers for a reliable completion boundary.

## Files and command output

### Optional native mounts

Build the native helper alongside the CLI:

```sh
make native
./bin/afs mount shared ./live --backend=fuse
# Or:
./bin/afs mount shared ./live-nfs --backend=nfs
./bin/afs status ./live
./bin/afs unmount ./live
```

Native mounts also accept `--readonly`, which denies writes through the native
filesystem. FUSE can present files as a chosen user/group and admit other local
users, useful when a privileged helper serves a non-root workload:

```sh
afs mount shared ./live --backend fuse --uid 1000 --gid 1000 --allow-other
```

These three options require FUSE. Omitted ownership values use the invoking
user's ownership defaults; zero is an explicit root override. Access by other
users remains disabled unless `--allow-other` is supplied and permitted by the
system's FUSE configuration. Shared mounts enforce the displayed POSIX permissions
in the kernel, so `--allow-other` does not bypass file access restrictions.
Native read-only unmount does not report an upload
receipt.

Native mountpoints must be empty. FUSE requires the platform's installed FUSE
driver; NFS requires `mount_nfs` on macOS or `mount.nfs` on Linux and OS mount
privileges. Install `afsmount` alongside `afs`, or set `AFS_NATIVE_HELPER` to its
absolute path. The ordinary CLI does not link the driver libraries.

Native mounts expose Redis files directly. Unmounting does not copy them onto
the local disk. They share workspace/checkpoint safeguards and Redis Array
detection with folder sync, but native concurrent writes do not create sync
conflict copies. See [native mount semantics and testing](docs/native-mounts.md).

### Ordinary filesystem tools

Use ordinary filesystem tools inside a mounted directory:

```sh
afs mount shared ~/agent-a
mkdir -p ~/agent-a/documents
printf 'exact bytes\n' > ~/agent-a/documents/today.txt
cat ~/agent-a/documents/today.txt
cp ./image.bin ~/agent-a/image.bin
mv ~/agent-a/documents/today.txt ~/agent-a/documents/yesterday.txt
rg 'exact' ~/agent-a
rm ~/agent-a/documents/yesterday.txt
```

Folder synchronization publishes these changes and receives changes from other
clients. Use `afs cp create` or normal `afs unmount` when you need a completed
local flush. There is no separate remote-file command group.

Commands print readable text by default: tables for lists, labeled details for
`info`, `cp show` and `status <directory>`, and short confirmations for changes.
Database commands show a credential-free Redis URL header, including the effective
database number. `status` labels the configured endpoint and lists each mount's
Redis endpoint; targeted status and unmount identify the mount's database.
JSON output keeps its existing schema without a text header.
Displayed timestamps use `dd/mm/yyyy hh:mm:ss AM/PM` (12-hour time) in the system's local timezone
(including a `TZ` environment override). JSON and stored timestamps retain their
machine-readable formats.

Use `--json` explicitly when piping structured results into scripts:

```sh
afs list                   # readable table
afs --json list            # JSON array
afs status ~/agent-a       # connection, pending work, conflicts and errors
```

Workspace actions are `create`, `list`, `info`, `fork` and `delete` at the root,
alongside `mount`, `unmount` and `status`. Optional file history stays under
`history`: `list`, `show`, `diff`, `restore`, `undelete`, `export`, `policy` and `serve`.
Checkpoints stay under `cp`:
`afs delete shared` deletes a workspace; `afs cp delete shared old-checkpoint`
deletes a checkpoint. Both retain their confirmation and safety checks.
The former `ws` prefix and `fs` group are removed, with no compatibility aliases.

## Checkpoints and forks

```sh
afs cp create shared --name before-refactor
afs cp list shared
afs cp show shared before-refactor
afs fork shared experiment --checkpoint before-refactor
afs fork shared latest-experiment
```

Forks have independent trees and checkpoint/blob ownership. Omitting
`--checkpoint` selects the most recently created checkpoint. Workspace creation retains
the original `initial` seed checkpoint; file edits do not move that checkpoint.
Create a checkpoint to fork newly published work. Conflicts retain the original
best-effort automatic conflict checkpoint behavior.

`cp create` first flushes this machine's registered mounts of that workspace and
Redis connection. A stopped mount or failed flush aborts it. Without a local
mount it snapshots published Redis state. It cannot include another machine's
unuploaded changes. Pause applications on all clients when application-consistent
contents matter; this is not a distributed filesystem transaction.

```sh
afs unmount ~/agent-a
afs cp restore shared before-refactor --yes
afs cp delete shared old-checkpoint --yes
afs delete experiment --yes
```

Restore requires local mounts to be unmounted and retains the existing safety
checkpoint. Other running clients become stale and report an error; their pending
local content stays available. After restore, preserve any needed edits and mount
into a **new directory**. A saved baseline from before restore is rejected on
restart. Deleted workspace IDs cannot be recreated by stale clients.

Head/default checkpoints cannot be deleted. Checkpoint deletion retains blob
bodies conservatively; there is no garbage collector. Workspace deletion removes
its own data while independent forks remain readable. Destructive commands prompt
at a terminal or require `--yes` when used noninteractively.

## Per-file history

File history is off by default. Upgrade every writer before enabling it; the
policy is shared by the workspace in Redis and applies to all upgraded clients.

```sh
afs history policy shared --mode all --max-versions 100
afs history list shared documents/today.txt
afs history export shared documents/today.txt --version 2 --to ./recovered-today.txt
```

History captures published file and symlink mutations, including deletions,
renames and permission changes. It preserves the existing contents before the
first tracked overwrite or deletion, without creating a checkpoint. It does
not capture every transient local write.

Export creates a new local file or symlink and refuses an existing destination.
Inspect it with ordinary tools, then copy it into a mounted directory to publish
the recovered contents. To recover a deleted file, omit `--version` to select
its latest recoverable version. `history list` groups retained versions by file
ID; pass an earlier ID to `history export --file-id` when a path has been deleted
and recreated. Follow `--cursor` to read subsequent history pages.

Inspect content, compare versions, or publish a historical version:

```sh
afs history list shared documents/today.txt --order desc --limit 50
afs history show shared documents/today.txt --version '<version-id>'
afs history diff shared documents/today.txt --from-version '<version-id>' --to-ref head
afs history restore shared documents/today.txt --version '<version-id>'
afs history undelete shared documents/deleted.txt
```

Restore and undelete publish a new version into the workspace after checking for
concurrent changes. The optional `afs history serve` adapter supports the original web
history drawer; see [CLI and web UI compatibility](docs/file-history-compatibility.md).
The command spelling intentionally consolidates these actions under `history`;
the original HTTP interfaces remain compatible. There are no separate root
`recover`, `versioning`, `file` or `serve` commands or hidden aliases.
The Redis namespace and history storage format are separate from the original
project; this interface compatibility does not migrate original stored histories.

Identical contents share immutable history bodies, and retention reclaims bodies
after their final history reference is removed. A small change producing new
contents still retains a full-file snapshot, so frequent native writes to large
files can be expensive. Path filters, age/count/byte limits, metadata-only
large-file records and pagination are described in [file history](docs/file-history.md),
including fork/restore behavior and retention guarantees. See the
[measured comparison](docs/file-history-validation.md) for benefits and costs.

## Conflicts and recovery

With folder sync, disjoint edits converge automatically. When both sides modify one path, the
published version remains at the original path and the competing local version
is preserved as `name.conflict-<host>-<timestamp>-<counter>`. Review and merge those
files with normal filesystem tools. Concurrent delete/edit likewise preserves
modified local content as a conflict copy. Rename is synchronized as a path
mutation, with recovery preserving a changed destination if the old path vanishes.

Connection loss triggers retry and reconciliation. Crash recovery loads the
persisted baseline and reconciles the directory with Redis. A missing or unreadable
root is an error, never a request to delete the remote tree. Status exposes
connection state, observed queue counts, conflicts and the most recent error;
queue counts alone do not prove synchronization. A successful flush receipt does.

AFS preserves regular files, directories, symlinks and supported permission modes.
Folder sync checks permissions on files and directories; symlink targets sync,
while symlink permissions remain platform-specific.
It retains `.afsignore`, original temporary-file ignores, and the default 2 GiB
sync file cap; optional tuning is documented in the [report](docs/simplification.md).
Folder sync does not capture every transient application write or provide arbitrary
POSIX transactions. Redis durability depends on your server's persistence policy;
a flush receipt confirms publication, not a Redis disk fsync.

## Verify

To test **two actual systems against your existing remote Redis**, use the
[two-system sync runner](docs/two-system-sync.md). It coordinates both hosts,
checks exact bytes and permissions, injects client partitions/crashes, and saves
per-host failure reports in isolated test workspaces:

```sh
export AFS_TEST_REDIS='rediss://default:PASSWORD@HOST:PORT/0'
python3 scripts/two_system_sync.py host --binary "$(command -v afs)"
# On the other system, follow the SSH/join commands printed by the runner.
```

The repository's local test suites start disposable Redis servers and use separate
directories and state for independent client processes. `redis-server` must be on PATH.

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...
go test -tags=integration -timeout=15m -count=1 -v ./tests/e2e
```

`make cli-test` runs the complete reduced CLI acceptance suite.
`make check` runs build, vet, unit/race, native dependency regressions, CLI
acceptance and the core concurrency smoke test. Native kernel acceptance and
prior/current CLI comparison have separate commands below and in their guides.
The integration tag builds a fresh CLI binary
and executes the reduced commands as a black-box contract, including the README
workflows. The [CLI comparison](docs/cli-compatibility.md) maps prior commands to
AFS and describes the separately executable comparison against the prior binary.
Optional Redis Array tests require a separately configured disposable server;
set `AFS_TEST_ARRAY_REDIS_ADDR` to its host and port, and
`AFS_TEST_ARRAY_REDIS_SERVER` to the Array-capable `redis-server` executable for
tests that start their own instance. Skips are reported separately from
verification. CI runs the core, process and native kernel suites on Linux.

For agent fleets sharing one workspace, the [multi-writer lab](tests/multiwriter/README.md)
runs configurable independent clients, concurrent file/symlink conflicts,
partitions, crashes and cold hydration against disposable Redis. It retains
logs, expected versions, manifests and JSON/Markdown reports:

```sh
make multiwriter LAB_ARGS='--clients 4 --files 20 --rounds 3 --seed 1'
```

The lab includes a Linux Docker Compose environment. These process tests model
independent clients; validate actual microVM boot, disks and resource limits in
the customer's environment before drawing deployment capacity conclusions.
See the [recorded findings](docs/multiwriter-results.md) for reproduced bugs,
fixes, validation results and measurement limits.

License: GNU AGPL v3; see [LICENSE](LICENSE) and [NOTICE](NOTICE).
