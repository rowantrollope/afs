# Native workspace mounts

Native mounting is optional. `afs mount` defaults to folder sync; selecting
`--backend=fuse` or `--backend=nfs` starts the separate `afsmount` helper.
Workspace actions remain at the root and checkpoints remain under `afs cp`.

## Encapsulation

```text
cmd/afs                 workspace CLI, registry, native helper dispatch
internal/mountcontrol   authenticated local helper protocol; no driver imports
cmd/afsmount            private bootstrap and process lifecycle
mount/native            fixed FUSE/NFS runtime and OS mount operations
mount/internal/afsfs    retained FUSE adapter
mount/internal/nfsfs    retained NFS adapter
mount/internal/client  one shared Redis storage engine
third_party/go-nfs      retained patched NFS protocol implementation
third_party/go-fuse     FUSE dependency with bounded startup cancellation
```

The code stays in one Go module. `cmd/afs` imports only the helper protocol,
so native driver libraries do not become dependencies of the CLI executable.
The original control-plane observer, cloud dependencies and search worker are
not part of either native adapter.

The storage client exposes native inode operations through a separate interface.
Range writes stage changes in Redis and atomically publish content, metadata,
revision and change events against the observed inode and workspace generation.
Concurrent native operations retry against fresh state; expected-stat writes
from folder sync retain their conflict checks. Redis Array remains automatic
when supported, with Redis strings as the fallback. Byte-range writes avoid
downloading and reuploading entire files.

Each native session pins its workspace generation, including reads served from
client or adapter caches. Handles identify inodes across renames; namespace
mutations also check the parent directory identities at commit. Directory
creation publishes the requested permissions atomically, including mode `000`.

## Installation and lifecycle

```sh
make native
./bin/afs mount shared ./live --backend=fuse
./bin/afs mount shared ./network-files --backend=nfs
./bin/afs cp create shared --name before-change
./bin/afs status ./live
./bin/afs unmount ./live
./bin/afs unmount ./network-files
```

Both binaries must be built for the host platform. Put `afsmount` alongside
`afs`, put it on PATH, or supply an absolute `AFS_NATIVE_HELPER` path. FUSE
requires the platform driver and mount utility. NFS uses the OS NFSv3 client,
`mount_nfs` on macOS or `mount.nfs` on Linux, with the necessary mount privileges.
AFS does not install drivers or change system mount policy.

Use `--readonly` for a receive-only FUSE or NFS mount. Native adapters reject
mutations, and unmount reports `read_only: true` without claiming an upload/save
receipt. For FUSE, `--uid` and `--gid` select the presented file ownership;
explicit zero is supported. `--allow-other` permits access by other local users
when the host FUSE policy allows it. These three options require `--backend=fuse`:

```sh
./bin/afs mount shared ./live --backend=fuse --uid 1000 --gid 1000 --allow-other
./bin/afs mount shared ./observer --backend=fuse --readonly
```

Ownership overrides are local presentation settings, not a Redis ACL or a
per-file ownership database. Omitting them preserves the process ownership
defaults. Read-only folder sync is also available; its local permission and
offline-edit behavior is described in the [README](../README.md).

The mountpoint must be an empty directory, or have an existing parent so the
helper can create it. Native mounts overlay their directories. Unmounting does
not hydrate files onto the local disk. A helper-created directory is removed
only when it is empty after detaching; pre-existing directories are preserved.

The same registry tracks all backends. Checkpoint creation flushes locally
registered mounts; restore and delete require them to be unmounted. Native
flush pushes kernel buffers through the adapter and joins storage operations.
It confirms visibility in Redis, not that Redis has fsynced its persistence
files. Applications must pause writes for an application-consistent checkpoint.

Native control sockets, process ownership and bootstrap files live outside the
mounted tree. Bootstrap files have mode 0600; Redis credentials are not passed
as command arguments. Control requests authenticate the exact mount identity.
Socket names remain short even when the configured state directory is long.
The registry records the mount identity before launching the detached helper,
so an interrupted startup remains discoverable and recoverable with `unmount`.
Driver startup has a 30-second deadline, including mount utility execution and
FUSE descriptor handoff. If a mount exists and normal startup cleanup fails,
the helper retains authenticated control for recovery.

The NFS gateway listens only on loopback and serves one workspace. NFSv3's
AUTH_SYS/AUTH_NULL handling is not an authentication boundary against other
users on the same host; use it on a trusted host. The local CLI control socket
has separate capability authentication.

A failed normal flush or unmount leaves the helper serving. `unmount --force`
explicitly requests forced detach without a flush guarantee. It can also
recover a kernel mount after a helper crash; registry PIDs are never blindly
signalled. A restored or deleted workspace fences old native sessions so stale
file handles cannot write into the replacement tree.

## File and concurrency semantics

| Operation | Contract |
| --- | --- |
| Native range writes | A successful operation atomically publishes its range with metadata. Disjoint concurrent ranges compose; overlapping ranges serialize. |
| FUSE append | Append position is selected by the shared storage operation, including concurrent FUSE writers. |
| NFS append | NFSv3 WRITE carries an offset, not append intent. Coordinate concurrent append writers at the application level. |
| Folder sync and native clients | Both share the same tree and events. Folder-sync conflict copies remain part of asynchronous sync, not native write behavior. |
| Locks | FUSE exposes advisory locks through session-scoped owners and expiring leases. NFS is mounted with NFS locking disabled. |
| Rename with an open file | Inode identity is retained; a path hint must not redirect the handle to a replacement inode. |
| Unlink with an open file | Full POSIX open-unlink lifetime is not supported: unlink removes the inode's content. |
| Hard links and extended attributes | Not supported by the retained FUSE adapter. |
| NFS AppleDouble files | `._*` names use ordinary durable Redis entries. The original gateway's private RAM-only shadow is removed; folder sync's existing metadata ignore rules remain. |

Native mounts require connectivity for storage operations. They do not provide
folder sync's local offline working copy. Losing a lock-session lease fences that
native session until remount. Peer events invalidate caches, resubscription
clears missed-event state, and cache expiry bounds staleness when events are
missed. Cache hits do not extend their own lifetime. Clients can still observe
peer changes at different times; this is not a distributed transaction.

Linux flush uses `syncfs`. macOS walks regular files and directories and calls
`fsync` without reading file contents. Flush cost therefore grows with the
macOS tree size. Both paths then join admitted storage requests and validate the
native session; neither certifies Redis disk persistence.

## Acceptance

The existing process lab continues to exercise folder-sync conflict preservation,
partitions, crashes, deletion/recreation, symlinks and cold hydration. The native
lab reuses its disposable Redis, per-client configuration, network proxies,
exact-content oracles, retained reports and cold-observer checks:

```sh
make multiwriter LAB_ARGS='--clients 8 --files 5 --rounds 2 --seed 17 --latency-ms 1 --timeout 90'
make multiwriter-native LAB_ARGS='--clients 4 --backends fuse,nfs,sync --files 8 --rounds 3 --seed 17'
```

The native command needs working kernel mounts; unavailable prerequisites fail
the run. Its append scenario requires two FUSE writers. The default four-client
backend sequence is FUSE, NFS, sync, FUSE. Use repeated `--scenario` options to
select applicable cases for a different backend arrangement.

Native cases check ordinary files and permissions, concurrent disjoint and
overlapping writes, FUSE append, open-handle rename, missed-event recovery,
checkpoint flushing, stale handles after restore, FUSE advisory locks and native
helper crash recovery. Working clients and a fresh folder-sync observer must
reproduce the expected published tree under their documented filename policy.
Kernel runs use disposable directories and Redis, not an existing installation.
On macOS, the test compares the sync observer using its existing AppleDouble
ignore policy, and requires a separate fresh native mount to reproduce the full
tree, including the exact bytes of all `._*` sidecars. Native sidecar differences
and missing ordinary files still fail the test.

For a reproducible Linux kernel run, use Docker with FUSE and NFS support:

```sh
mkdir -p tests/multiwriter/artifacts
docker compose -f tests/multiwriter/compose.native.yaml run --build --rm native
AFS_LAB_CLIENTS=8 AFS_LAB_LATENCY_MS=1 \
  docker compose -f tests/multiwriter/compose.native.yaml run --build --rm native
```

The container needs `/dev/fuse` and `SYS_ADMIN` for its own mounts. It has no
external network at runtime and mounts no host configuration. Clients work on
native Linux tmpfs; only retained artifacts are copied to the host. The supervisor
bounds the whole run independently of kernel filesystem calls, records binary
hashes, and fails on timeout or uncertain cleanup. Its default container deadline
is 20 minutes; set `AFS_LAB_OVERALL_TIMEOUT` for larger workloads. The CI native
job runs both four and eight clients and retains reports even after failures.

### Recorded platform evidence

On September 15, 2026, macOS 26.5 on ARM64 with Redis 8.6.2 passed all eight
NFS-compatible scenarios using two native NFS clients: files and permissions,
disjoint ranges, overlapping writes, handle renames, reconnects, checkpoints,
helper crashes and generation fencing. Each scenario also checked a fresh
native mount's complete tree, including AppleDouble bytes, and a fresh sync
observer's tree under its ignore policy. FUSE-only append and lock cases were
not part of this NFS run.

The [macOS report](../tests/multiwriter/artifacts/native-macos-nfs-parent-guard-20260915/report.json)
and [supervisor record](../tests/multiwriter/artifacts/native-macos-nfs-parent-guard-20260915/supervisor.json)
record the tested binary hashes and a 418-second run with no timeout, cleanup
errors or remaining owned processes. The [workload log](../tests/multiwriter/artifacts/native-macos-nfs-parent-guard-20260915/workload.log)
records all eight passing cases on the recorded CLI and helper binaries.
LinuxKit 6.12.54 on ARM64 with Redis 7.0.15 passed all ten mixed FUSE/NFS/sync
scenarios at both [four clients](../tests/multiwriter/artifacts/final-acceptance-20260915/linux-native-four.json)
and [eight clients](../tests/multiwriter/artifacts/final-acceptance-20260915/linux-native-eight.json).
Both used eight files per writer, three rounds, seed 17 and 1 ms delay per proxy
read, with unchanged 90-second assertions and a 20-minute whole-run deadline.
The runs finished in 292 and 905 seconds respectively, with no timeout, capture
or cleanup errors. These are real kernel mounts with independent helpers and
Redis connections; each run shares one kernel and is not a microVM benchmark.

macOS FUSE kernel acceptance remains unverified. A subsequent September 15
two-client probe on macFUSE 5.1.3 timed out during startup after 30 seconds.
The macOS kernel manager explicitly reported that the macFUSE extension was
not approved to load and required approval in System Settings. Cleanup completed
with no remaining owned processes. Approve macFUSE in System Settings, restart
if prompted, then repeat native acceptance. This implementation uses the macFUSE
kernel backend; it does not select FSKit. Linux FUSE kernel acceptance includes
append, advisory locks and crash recovery.

Source compilation and adapter unit tests alone are not native mount acceptance.
See [multiwriter results](multiwriter-results.md) for the full integrated results.

`scripts/native_options_smoke.py` provides an additional isolated Linux FUSE
check for ownership/access options. Run it as root in the disposable native lab
container with `--afs /usr/local/bin/afs --helper /usr/local/bin/afsmount`. It
starts and verifies ownership of its own Redis process, mounts three clients,
drops to UID/GID 1000 for file operations, checks ownership after chmod, denies
read-only mutations and private-mount access, and verifies normal unmount and
owned cleanup.
