# Multiwriter agent workspace lab

This lab runs independent AFS clients against one shared workspace on a disposable
Redis server. Each client has its own process, HOME, configuration, state,
directory tree, and TCP proxy. The proxies let the runner interrupt one client's
connection while other clients continue working.

Use it to find convergence failures, lost file versions, corruption, and recovery
bugs before testing the customer's deployment. The Linux environment below runs
all clients in **one container**. It models independent agent processes; it does
not create microVMs or separate containers for each agent.

## Run locally

Requires Python 3, `redis-server`, `redis-cli`, and `ps` on PATH, plus Go 1.22.2 or
newer when building the binary. Run from the repository root:

```sh
python3 scripts/multiwriter_lab.py --clients 4 --files 20 --rounds 3 --seed 1
```

The runner builds `./cmd/afs`, creates a fresh temporary output directory, prints
its location, and retains the evidence after stopping its processes. It starts its
own Redis on a loopback address and an ephemeral port. There is no external Redis
option, and it does not use an existing AFS installation or the user's state.

To retain results at a chosen location or test an existing binary:

```sh
python3 scripts/multiwriter_lab.py \
  --binary "$PWD/bin/afs" \
  --output "/tmp/afs-lab-$(date +%s)-$$" \
  --clients 8 --files 100 --rounds 5 --seed 17
```

`--output` must name a new directory. `--timeout` is the deadline for each command
or convergence assertion in seconds (default 45), not a whole-run deadline.
`--latency-ms` adds delay per proxy read in each direction (default 0); it is an
application proxy delay, not an accurate network RTT model.

Select one or more cases with repeated `--scenario` arguments:

```sh
python3 scripts/multiwriter_lab.py \
  --scenario shared-edit --scenario partition --scenario crash \
  --clients 4 --rounds 5 --seed 17 --latency-ms 20 --timeout 90
```

Use at least two clients. `--files` controls pre-existing files per writer in
`hydrate` and new files per writer per `disjoint` round. `--rounds` repeats edits
in `hydrate`, the `disjoint` workload, the three shared-file cases, and concurrent
symlink creation; delete/recreate, partition, crash, rename/delete, and warm
symlink recovery run once per invocation. `delete-recreate` adds one new writer
after the initial clients have observed deletion. The runner uses atomic file
replacement for application saves, so this workload does not exercise in-place
writes.

Repeat seeds and larger client counts to explore scheduling variation. A seed
controls disjoint file sizes and labels all payloads; operating-system and network
scheduling can still change the interleaving.

## Run in Linux with Docker Compose

Requires Docker with Compose and a running daemon. Build uses network access to
fetch images, Go dependencies, and Debian packages. The running lab has only its
container's loopback network; no host port or Redis endpoint is exposed.

From the repository root:

```sh
mkdir -p tests/multiwriter/artifacts
export AFS_LAB_UID="$(id -u)" AFS_LAB_GID="$(id -g)"
docker compose -f tests/multiwriter/compose.yaml run --build --rm lab
```

Clients run on the container's native `/tmp` tmpfs. When the runner finishes,
an exit trap copies its complete output to
`tests/multiwriter/artifacts/run-<time>-<uuid>/`, preserving the runner's failure
exit code. Each run gets a fresh directory, so repeat runs retain earlier evidence.
The UID/GID settings keep bind-mounted artifacts owned by the invoking user on
Linux. Abruptly killing the container can prevent artifact export.

Running the workspace directly on a Docker Desktop host bind can invalidate
Linux file-lock behavior. In this environment, a two-process check confirmed
that the bind allowed both processes to acquire the same exclusive lock; native
`/tmp` correctly rejected the second lock. The artifact bind is therefore used
only to export results. Tmpfs exercises Linux synchronization semantics, but its
timings are **not a disk performance benchmark**.

Set workload parameters through environment variables:

```sh
AFS_LAB_CLIENTS=8 AFS_LAB_FILES=100 AFS_LAB_ROUNDS=5 \
AFS_LAB_SEED=17 AFS_LAB_LATENCY_MS=20 AFS_LAB_TIMEOUT=90 \
  docker compose -f tests/multiwriter/compose.yaml run --build --rm lab
```

Set `AFS_LAB_ARTIFACTS` to an existing absolute directory to store results
elsewhere. The default `./artifacts` bind path is relative to `compose.yaml`, not
the shell's current directory. No host AFS configuration is mounted.

To select cases, provide a space-separated list of scenario names:

```sh
AFS_LAB_SCENARIOS="shared-edit partition" AFS_LAB_CLIENTS=4 AFS_LAB_ROUNDS=5 \
  docker compose -f tests/multiwriter/compose.yaml run --build --rm lab
```

## Cases and interpretation

For the optional native-mount extension, use
`docker compose -f tests/multiwriter/compose.native.yaml run --build --rm native`.
It reuses this lab with real FUSE/NFS kernel mounts and a whole-run supervisor.
It builds and runs one `afs` executable for every backend. Native mount daemons
are child processes of that executable; `--binary` selects the same binary for
mounts and recovery. The supervisor records its hash in `supervisor.json`.
See [native mount acceptance](../../docs/native-mounts.md#acceptance) for
prerequisites, concurrency semantics and scenarios.

| Case | Pressure applied |
| --- | --- |
| `hydrate` | Concurrently mount an imported project with 4 KiB files, an executable, symlink, and empty mode-0750 directory; verify content/modes, then edit owned existing files. |
| `disjoint` | Writers create separate files, then delete, rename, or edit their own paths each round. |
| `shared-create` | Pause every daemon, create distinct versions at one new path, then resume together. |
| `shared-edit` | Publish and flush a common baseline, pause daemons, then apply competing replacements. |
| `chunked` | Repeat shared-edit with files of 1 MiB + 317 bytes to exercise chunked content. |
| `delete-recreate` | All initial clients observe deletion of a file and symlink; a new writer republishes identical bytes and target, which every client must retain after flush. |
| `partition` | Disconnect one client; race its local edit with a connected peer and preserve an offline new file. |
| `crash` | SIGKILL a stopped daemon with unpublished edits; advance a peer; remount the retained local tree and state. |
| `rename-delete` | Race delete against edit, rename against a competing destination, and a stale local rename against a peer's source edit. |
| `symlink` | Recover competing dangling-symlink targets after a crash, then race new symlink creation each round. |

The runner checks the working clients and a fresh observer mounted from Redis.
Fresh observation matters: matching warm client directories alone cannot prove
that Redis contains the expected data. Treat a failed assertion or timeout as a
reproducer to investigate, with the recorded configuration and retained files.

AFS is asynchronous folder synchronization. Disjoint edits should converge.
Concurrent writes to one path can produce a published version and conflict files;
they do not produce a merged document or transaction. Inspect preserved versions
as well as the canonical path. An application write is only local until AFS
publishes it; use a successful AFS flush operation when publication is required.
Publication does not itself prove Redis has fsynced the data to disk.

A green run demonstrates the checked invariants for those inputs and that
interleaving. It does not prove linearizable file access, arbitrary POSIX
transactions, preservation of every transient intermediate write, or throughput
on the customer's infrastructure.

## Evidence and next validation

Keep the complete output directory:

- `report.md`: scenario pass/fail summary and total scenario times.
- `report.json`: configuration, binary hash, source revision when available,
  labeled timings, assertion failures, tracebacks, and capture/cleanup errors.
- `redis/server.log` and Redis persistence files: disposable server evidence.
- `<scenario>/redis-info.txt`: server state captured at scenario completion.
- `<scenario>/operations.jsonl`, `expected.json`, and `candidates-*.json`:
  workload operations and the expected path states or preserved versions.
- `<scenario>/<client>/manifest.json`, `differences.json`, `status.json`, and
  `cli.jsonl`: file signatures, mismatches against the path oracle, daemon status,
  and CLI invocations with results and timing.
- `<scenario>/<client>/mount-<n>.log`: output from each supervised foreground
  mount, including warm remounts.
- `<scenario>/<client>/workspace/` and `state/`: retained files and daemon state;
  daemon logs are under `state/**/sync.log`.

Evidence files depend on how far a scenario progressed before failure. The
container has no Git checkout: source revision is reported as unavailable and
the binary SHA-256 identifies exactly what ran. Exported container reports retain
their original `/tmp/run-...` paths for provenance; locate the corresponding files
under the exported directory on the host.

Compare results at the same client count, file count, rounds, seed, and delay.
Convergence timing includes Python orchestration, verification overhead, and
local Redis configured with AOF and `appendfsync always`. Use timings to identify
regressions rather than as a production capacity claim. The reported p50/p95 are
over completed disjoint batches; the default three rounds provide only three
samples, not a robust latency distribution.

All local clients share one kernel, filesystem implementation, host clock, and
host identity. Proxy failures model connection interruption; they do not model
every packet-loss, DNS, TLS, routing, or VM scheduling condition. Process crashes
retain the local disk and do not model loss of an ephemeral microVM disk. The
disposable Redis setup is not a Redis replication or persistence test.

Before deployment, repeat representative workloads with one client per actual
microVM, the intended Redis topology and latency, production resource limits,
and the customer's boot, reconnect, shutdown, and disk-loss behavior. Preserve
the same file-version and cold-observer assertions when extending the lab.
