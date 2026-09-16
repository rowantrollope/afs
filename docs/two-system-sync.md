# Test synchronization between two systems

[`scripts/two_system_sync.py`](../scripts/two_system_sync.py) runs the same
coordinated acceptance suite on two macOS/Linux systems against **your existing
remote Redis server**. Use the same revision of this script and the adjacent
`multiwriter_lab.py` on both systems. Python 3.9+ and an AFS executable are required;
Redis does not need to be installed locally. SSH access from B to A carries the
small coordination connection. Both systems connect directly to Redis.

The runner creates a fresh workspace for each scenario, named
`two-host-<random-run-id>-<scenario>`, and creates separate local folders, configs,
and daemon state. It never flushes Redis, restarts the server, modifies existing
workspaces, or changes your installed AFS/configuration. It does generate real
Redis traffic and retain test data. Run on the filesystem/disk you want to test
by choosing `--output` on each system.

## Start a run

On **both systems**, from the AFS checkout, select the Redis URL:

```sh
export AFS_TEST_REDIS='rediss://default:YOUR_PASSWORD@YOUR_REDIS_HOST:6379/0'
```

Use `redis://` for a server without TLS. Percent-encode special characters in the
username/password. Both URLs must reach the same database; they may use different
hostnames or credentials. The runner verifies that both clients see the same
newly created workspace ID before mounting. TLS verifies the server certificate
and hostname; Python's `SSL_CERT_FILE` can select a private CA bundle. Mutual TLS,
Sentinel, and Redis Cluster discovery are not supported by this runner.

Build a separate executable on each system, or use an existing `afs` executable:

```sh
go build -o /tmp/afs-sync-test ./cmd/afs
# Alternatively, pass --binary "$(command -v afs)" below.
```

On **system A**:

```sh
python3 scripts/two_system_sync.py host --binary /tmp/afs-sync-test
```

A prints two commands for B, including a new pairing token. On **system B**, run
the printed SSH command in a separate terminal, substituting A's SSH address:

```sh
ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 \
  -L 18765:127.0.0.1:18765 USER@SYSTEM_A
```

Leave that terminal open. In B's AFS checkout, with `AFS_TEST_REDIS` exported,
run the **exact join command printed by A**:

```sh
AFS_TEST_TOKEN=TOKEN_PRINTED_BY_A python3 scripts/two_system_sync.py join \
  --binary /tmp/afs-sync-test
```

The suite starts once both systems join. A controls the scenarios and workload
size; B receives those settings automatically. No synchronized clocks, manual
phase timing, Redis admin access, or inbound Redis connection to A is needed.
A waits up to 15 minutes for B by default. Only one runner may occupy each role.

Port 18765 is the loopback coordination port, not a Redis port. If it is busy,
choose another with `host --control-port 28765` and use A's updated commands.
If only B's local port is busy, forward `28765:127.0.0.1:18765` and pass
`join --control-port 28765`. Switch A/B roles if SSH is available only in the
opposite direction. The coordinator deliberately does not bind a public address.

## What it checks

To pass, every scenario must finish with serialized successful checkpoint flushes, a fresh
observer on **each** system with an empty directory and separate state, and a
normal unmount that must retain the local files. Fresh observers verify that
versions reached Redis, rather than merely surviving on their originating host.

| Scenario | Validations |
|---|---|
| `basic` | Both directions; empty/binary/hidden files, spaces/punctuation/Unicode names, executable/read-only files, empty/deep directories, relative/dangling/directory symlinks, edits originating on the other host |
| `mutations` | Recursive rename/delete, identical file/symlink recreation, append, truncate, file chmod and directory chmod after rename |
| `bulk` | Concurrent disjoint creation, in-place overwrite, rename and deletion across repeated batches; unexpected conflict copies fail |
| `large` | Files above the chunk threshold, edits crossing 256 KiB and 1 MiB boundaries, shrink below threshold, zero-length truncation, and regrowth |
| `ignore` | Different local `.afsignore` fixtures remain private, survive restart, and never appear in a cold observer |
| `shared-create` | Competing creation from stopped daemons; every intended file version must appear on both hosts |
| `shared-edit` | Competing edits to an established large file; preserve both complete versions |
| `symlink-conflict` | Competing symlink targets survive as canonical/conflict entries |
| `delete-edit` | A published deletion racing an unpublished peer edit must preserve that edit |
| `rename-edit` | Rename versus unpublished edit must retain the baseline and edited bytes under valid source/destination/conflict paths |
| `partition` | Close B's existing Redis connections and reject new ones while A keeps publishing; reconnect and preserve both file and symlink versions plus disjoint offline files |
| `crash` | Stop B, write unuploaded changes, SIGKILL its owned daemon, publish changes from A, then warm restart B and recover all intended versions |

Manifests check **exact path sets, types, byte counts, SHA-256 digests, permission
modes, and symlink targets** against expectations derived from intended writes.
Equal-but-wrong trees fail. Missing versions, stale resurrected files, unexpected
entries/conflicts, read errors, failed CLI commands, and peer timeouts also fail.
Conflict filenames may vary, but every required version must survive and the
complete trees must agree. Each observation must remain stable for two seconds
by default. Local timers are used; wall-clock synchronization is unnecessary.

Transient intermediate application writes are not required to survive. Ownership,
mtimes, xattrs, ACLs, hardlinks, sparse allocation, case-only rename behavior,
power-loss durability, native FUSE/NFS semantics, server restart/failover, and
exhaustive scheduling interleavings are outside this suite. The partition affects
only B's test client proxy; your normal AFS processes and Redis stay online.

## Increase load or repeat a finding

On A:

```sh
python3 scripts/two_system_sync.py host --binary /tmp/afs-sync-test \
  --files 1000 --rounds 10 --large-mib 32 --seed 17 \
  --timeout 300 --output "$HOME/afs-test-a-$(date +%s)"
```

Pass a **new** `--output` directory to B too when testing a particular disk.
`--files` means files per writer per bulk round. The default is 100 files,
three rounds, and an 8 MiB large-file fixture. Conflicts repeat `--rounds` times.
The seed controls bytes, not OS/network scheduling. Repeat runs to exercise
different interleavings. Large-file expectations are held in memory, so choose a
size appropriate to both machines.

Use repeated `--scenario` options to narrow a run:

```sh
python3 scripts/two_system_sync.py host --binary /tmp/afs-sync-test \
  --scenario mutations --scenario partition --scenario crash --timeout 180
```

`--watcher-queue 8 --files 1000` stresses watcher overflow/recovery. This setting
applies only to newly created test mounts. `--latency-ms 20` on either runner adds
delay per proxy read in both directions; it is not an accurate RTT emulator or
network benchmark. `--timeout` bounds each CLI command, barrier, and convergence
attempt, not the whole suite. A slow or overloaded server can cause a timeout;
inspect the saved differences before concluding that data is lost.

## Results and retained data

Both systems print their artifact directory at startup, progress for every
scenario, and a final `PASS` or `FAIL`. **Exit 0 means every selected scenario and
cleanup check passed; exit 1 means failure or an incomplete run.** A scenario
failure releases the other peer and the runner continues with a fresh workspace
for the next scenario. A lost coordination channel or interrupted runner can end
the run early; the report remains incomplete and returns nonzero.

Each output directory contains:

- `report.md` and `report.json`: per-scenario outcomes, durations, both hosts'
  platform/binary information, settings, retained workspace names, and cleanup errors.
- `<scenario>/oracle.json`: independent expected entries and required conflict versions.
- `<scenario>/check-NNN.json`: most recent observation for that phase, both
  manifests, and exact expected/actual differences. Checks during a simulated
  outage observe only the connected host.
- `<scenario>/operations.jsonl`, `failure.txt`, per-client `cli.jsonl`, mount logs,
  saved daemon state, and the actual writer/cold-observer directories.

The output directory is private (`0700`). Client configurations contain your
Redis credentials (`0600`); **do not share the whole artifact directory without
removing credentials and local control tokens**. `report.md`, `report.json`,
`oracle.json`, and `check-*.json` do not contain the Redis password. CLI/mount logs
and state should be reviewed before sharing.

The runner stops only its own processes. It deliberately retains its new Redis
workspaces/checkpoints and local artifacts for inspection. Once both runners
have exited and you have finished debugging, remove each exact workspace listed
in `report.json` using your normal configured CLI:

```sh
afs delete two-host-EXACT_RUN_ID-basic --yes
```

Repeat for the other recorded names. There is no wildcard cleanup or database
flush. Delete the corresponding output directories when finished and close the
SSH tunnel. `Ctrl-C`/SIGTERM attempt owned-process cleanup; a kill of the runner
itself or a machine failure may require manual cleanup of that run's daemons.

## Local harness validation

The harness regressions run with the existing lab tests:

```sh
python3 -m unittest discover -s tests/multiwriter -p 'test_*.py'
```

They verify rejection of unanimous corruption/loss, missing conflict candidates,
wrong permissions, unexpected paths, missing roots/peers, mismatched phases and
scripts, duplicate participants, failed-case continuation, and real proxy
partition/reconnect behavior. A TLS regression checks trusted certificates,
original hostnames, and rejection of untrusted/mismatched certificates; it uses
`openssl` to generate a temporary certificate and skips if unavailable.
The `host`/`join` process workflow can also run on
one machine against a newly started disposable Redis using different output
directories and no SSH tunnel. That checks the harness, not cross-system behavior.

Local validation on 2026-09-16 used two macOS processes and disposable Redis with
the CLI built from `15ef038a238fa6b2d531c55d539a19bffd643390`. Both complete runs
reported **11 passed, one failed** and exited 1 on both peers. The repeated
failure is `mutations`: after renaming a directory, chmod to `0750` remained
`0755` on the other peer after 45 seconds. Content checks passed. This assertion
is retained; no production fix is included in the test-runner change.

The larger run used the default 100 files/writer/round, three rounds and 8 MiB
fixture, with `--timeout 45 --settle 0.5`. Both sides completed cleanup. An
unrelated pre-existing test workspace retained its metadata and cold-hydrated
bytes. Build, vet, unit/race and CLI integration checks passed. An additional
run of the existing local lab had one rename/delete candidate-preservation
timeout; its focused rerun passed. That intermittent result remains unresolved.
No real cross-host run or connection to the user's remote Redis was performed.

The subsequent upstream-audit follow-up fixes live directory chmod. Two fresh
paired-process runs pass all 12 scenarios, including the unchanged `mutations`
assertion; the final run uses the final follow-up binary with the workload and
deadlines above. Both peers exit 0, cleanup completes, and the pre-existing
fixture retains its metadata and cold-hydrated bytes. See
[follow-up implementation results](audit-followup-results.md) for binary hashes,
additional regressions and platform limits.
