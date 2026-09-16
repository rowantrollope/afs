# Mac/Sancho acceptance on the replacement Redis database

Run `a7145c3da397` passed **all 12 scenarios on both actual systems**. Both runners
exited 0, every saved tree comparison was clear, and both cleanup reports were
empty. The run used the same fixed binaries, source and workload as the previous
capacity-limited run, with the user-selected replacement Redis database.

## Results

| Scenario | Mac | Sancho | Mac seconds | Sancho seconds |
|---|---|---|---:|---:|
| basic | Pass | Pass | 45.05 | 45.32 |
| mutations | Pass | Pass | 35.51 | 35.79 |
| bulk | Pass | Pass | 447.60 | 447.62 |
| large | Pass | Pass | 38.91 | 39.09 |
| ignore | Pass | Pass | 19.14 | 19.18 |
| shared-create | Pass | Pass | 21.69 | 22.02 |
| shared-edit | Pass | Pass | 46.65 | 46.68 |
| symlink-conflict | Pass | Pass | 24.03 | 24.21 |
| delete-edit | Pass | Pass | 20.73 | 20.78 |
| rename-edit | Pass | Pass | 20.95 | 21.37 |
| partition | Pass | Pass | 23.37 | 23.22 |
| crash | Pass | Pass | 23.07 | 23.13 |

Each case completed checkpoint saves, fresh hydration on both machines and normal
unmount with the intended local tree retained. Validation compares against
independent expected contents and metadata, including preserved conflict versions;
agreement between the writers alone is insufficient. All 76 saved Mac comparisons
and 80 Sancho comparisons contain no discrepancies. Sancho has additional local
checks while the other writer is suspended or disconnected.

Specific coverage:

- Bidirectional empty/binary files, hidden and Unicode names, executable/read-only
  permissions, empty/nested directories and relative/dangling/directory symlinks.
- Reverse-direction changes; recursive rename/delete; identical recreation;
  append/truncate; file and directory chmod.
- Three bulk rounds of 100 files per writer: 600 file creations followed by
  deletion, rename and overwrite operations. All six bulk mutation-stage checks
  completed. Creation took 69.9–73.3 seconds per round and mutation 39.9–41.9 seconds,
  within the original 120-second phase deadline.
- Large files of 8 MiB plus 317 bytes per writer; edits crossing chunk boundaries;
  truncation to approximately 1 MiB and zero; regrowth beyond the initial size.
- Ignored/private files preserved locally through warm restart and excluded from
  fresh observers.
- Concurrent create/edit and symlink conflicts with both versions retained;
  deletion/edit and rename/edit races.
- Forced Redis disconnection and SIGKILL/warm restart, preserving competing file
  and symlink changes and disjoint offline files.

The bulk case includes many separate phases, so its total duration exceeds the
per-phase limit. Its Mac checkpoint completed in 32.38 seconds; fresh-observer
and writer unmount completed in 28.56 and 28.30 seconds. Sancho's corresponding
operations took 8.19, 6.91 and 8.54 seconds. All returned 0.

## Comparison with the previous run

Run `9cd72d09b414` on the previously configured database had five passes, six
OOM-blocked cases and one fresh-observer connection failure. All seven previously
incomplete scenarios now pass using identical tested code and workload. The five
previously passing scenarios also pass again.

The replacement database removed the resource failures observed in that run.
The directory-mode, symlink save/unmount, bounded-queue and small-file save fixes
now have a complete Mac/Linux acceptance pass, including the full bulk workload.
No production changes or relaxed limits were introduced during this rerun.

## Redis observations

Both systems reached `dogs-trees-lapis-80378.db.redis.io:15163`, database 0.
Credentials were supplied through private test-only configuration; neither
machine's saved configuration was changed.

| Server metric | Before run | After run |
|---|---:|---:|
| Used memory, bytes | 4,312,384 | 141,979,200 |
| Peak used memory, bytes | 4,996,880 | 157,731,376 |
| Rejected connections | 0 | 0 |
| Evicted keys | 0 | 0 |
| Total error replies | 0 | 0 |
| Configured client limit | 256 | 256 |
| Eviction policy | noeviction | noeviction |

No test-owned OOM evidence was found on either host. All capacity-evidence lists
are empty. The service did not expose `maxmemory`, so these observations do not
establish the configured memory quota or remaining headroom. Counters are
server-wide. Test workspaces and checkpoints were retained for review.

## Provenance and cleanup

- Source: `ac31fda403bcc1bfd0733968954dc1679f603d76` plus the uncommitted fixes.
- Full frozen source SHA-256:
  `b630425763c3062a29dd873ba1582608fcbdd87b7d571882a78e35bf6410b1d3`.
- Runner-source handshake SHA-256:
  `f750333fcb72edf5f262b1f9c2d79131522bda82e4b8f27367cf83f594f014b0`.
- Mac native binary SHA-256:
  `9b922bc279d4ade5db9a6a1d36d28e5429d6ceea567a841f2fd51c87f6775f43`.
- Sancho native binary SHA-256:
  `412eda869988c9c8a9c6c4e0f26e6b8503b6e88272cf8cf3179caf784426f773`.
- Platforms: macOS 15.8 arm64 and Linux 6.17.0-1019-aws x86-64.
- Workload: 100 files/writer/round, three rounds, 8 MiB large fixtures, seed 1,
  two-second stability window, watcher queue 1024 and 120-second phase limit.
- Existing verified native binaries were copied into new private run directories;
  frozen source contents and binary hashes were rechecked on both hosts. Current
  production/runner/Go test files also match the frozen source.
- Sancho ran `host`; the Mac ran `join` through a new loopback SSH tunnel. Every
  scenario used a fresh `two-host-a7145c3da397-*` workspace.
- Both cleanup reports are empty. Independent process checks found no test-owned
  AFS processes on either system. The Mac runner and tunnel PIDs are gone, and
  Sancho recorded runner exit 0. Installed binaries, saved configuration, previous
  workspaces and the user's terminal sessions were preserved.

## Retained evidence

- Mac artifacts:
  `/var/folders/kg/0cp9s_gx4v5bjkc6bkmcx3f40000gn/T/afs-sancho-newdb-sqtqus9d`.
- Sancho artifacts: `/tmp/afs-sancho-newdb-sqtqus9d`.
- Mac report: `mac/report.json`; fetched Sancho report:
  `sancho-results/report.json`, under the Mac artifact root.
- Selected Sancho diagnostics include logs, expected trees, comparisons and sync
  state, excluding configurations and local control directories.
- Credential-free report copies, provenance, capacity snapshots and cleanup proof:
  `tests/multiwriter/artifacts/two-system-a7145c3da397-review/` (gitignored).

Full artifact directories contain private connection configuration. The copied
reports and this evaluation are the review materials. This result covers one
complete default two-host folder-sync run; extended soak and higher-writer-count
testing remain separate coverage.
