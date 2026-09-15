# AFS

Persistent agent workspaces backed by Redis. Mount the same workspace on two
machines and edit ordinary local files; changes synchronize in both directions.
One workspace owns one file tree and its checkpoints.

AFS is a slim Go derivative of [redis/agent-filesystem](https://github.com/redis/agent-filesystem),
retaining its folder sync, inode storage, manifest checkpoints and recovery code.
See the [simplification report](docs/simplification.md) for provenance and changes.

## Build

Requires Go 1.22.2 or newer and a Redis server. Supported local platforms are
macOS and Linux; acceptance runs cover Redis 7.0.15 and 8.6.2. No FUSE, NFS, control-plane server or agent plugin is required.

```sh
git clone https://github.com/rowantrollope/afs.git
cd afs
go build -o bin/afs ./cmd/afs
./bin/afs --help
```

Use the binary directly or put it on your PATH. Do not replace an existing
`agent-filesystem` installation unless you intend to do so.

## Configure

Pass a Redis URL with each command:

```sh
afs --redis redis://localhost:6379/0 ws create demo
```

Or put this in `~/.config/afs-lite/config.json` (or select a file with `--config`):

```json
{"redis":"redis://localhost:6379/0"}
```

`--redis` overrides the file. URLs support username/password, a database number,
and TLS via `rediss://`. Protect configuration files containing credentials.
Help and version work without Redis.

Redis keys use `afs-lite:`; local state uses `~/.afs-lite`. These do not reuse the
original project's namespace or registry. `AFS_STATE_DIR` selects another local
registry/state directory, useful for isolated client tests. Each mount has its own
saved baseline and private daemon log beneath that directory.

## Two writable clients

Both machines must reach the **same Redis database**. First create the workspace:

```sh
afs ws create shared
# Or import a directory using the original parallel import machinery:
afs ws create project --from ./existing-project
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

## Files

```sh
afs fs ls shared
printf 'exact bytes\n' | afs fs put shared notes/today.txt
afs fs put shared image.bin --from ./image.bin
afs fs cat shared image.bin > ./download.bin
afs fs mkdir shared documents
afs fs mv shared notes/today.txt documents/today.txt
afs fs rm shared documents --recursive
```

Paths are workspace-relative. File commands use the same Redis mutation path as
sync; mounted clients receive their changes. `cat` writes exact bytes, including
binary and empty files. It rejects `--json`; other commands accept `--json` for
machine-readable results. Errors go to stderr.

## Checkpoints and forks

```sh
afs cp create shared --name before-refactor
afs cp list shared
afs cp show shared before-refactor
afs ws fork shared experiment --checkpoint before-refactor
afs ws fork shared latest-experiment
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
afs ws delete experiment --yes
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

## Conflicts and recovery

Disjoint edits converge automatically. When both sides modify one path, the
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
It retains `.afsignore`, original temporary-file ignores, and the default 2 GiB
sync file cap; optional tuning is documented in the [report](docs/simplification.md).
Folder sync does not capture every transient application write or provide arbitrary
POSIX transactions. Redis durability depends on your server's persistence policy;
a flush receipt confirms publication, not a Redis disk fsync.

## Verify

Tests start disposable local Redis servers and use separate directories and state
for independent client processes. `redis-server` must be on PATH.

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...
go test -tags=integration -timeout=15m -count=1 -v ./tests/e2e
```

`make cli-test` runs the complete reduced CLI acceptance suite.
`make check` runs all checks. The integration tag builds a fresh CLI binary
and executes the reduced commands as a black-box contract, including the README
workflows. The [CLI comparison](docs/cli-compatibility.md) maps prior commands to
AFS and describes the separately executable comparison against the prior binary.
Optional Redis Array tests require a separately configured disposable server;
skips are reported separately from verification. CI runs the core and process
suites on Linux.

License: GNU AGPL v3; see [LICENSE](LICENSE) and [NOTICE](NOTICE).
