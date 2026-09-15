# CLI compatibility with the original AFS

The compatibility suite runs two real CLI binaries against separate disposable Redis servers. It compares the extracted CLI with original commit `c3897ac05265444568a3819c21728a38a4ed254b`, using the original **volume** commands for the retained file-tree model. The original `ws` and top-level `mount` commands compose Agent Workspaces; those are different operations.

## Command mapping

| Retained operation | Original CLI | Extracted CLI |
| --- | --- | --- |
| Create empty file tree | `vol create NAME` | `ws create NAME` |
| Import directory | `vol import NAME DIR` | `ws create NAME --from DIR` |
| List / inspect | `vol list --json` / `vol info NAME` | `--json ws list` / `--json ws info NAME` |
| List directory | `fs NAME ls --json PATH` | `--json fs ls NAME PATH` |
| Read text / empty file | `fs NAME cat PATH` | `fs cat NAME PATH` |
| Fork checkpoint head | `vol fork SOURCE NEW` | `ws fork SOURCE NEW` |
| Create named checkpoint | `cp create --volume NAME LABEL` | `cp create NAME --name LABEL` |
| List / inspect checkpoint | `cp list NAME` / `cp show NAME LABEL --json` | `--json cp list NAME` / `--json cp show NAME LABEL` |
| Restore checkpoint | `cp restore NAME LABEL` | `cp restore NAME LABEL --yes` |
| Sync ordinary local folder | `vol mount NAME DIR` | `mount NAME DIR` |
| Flush before checkpoint | `vol save --json DIR`, then `cp create` | `cp create` flushes registered local mounts |
| Stop sync, preserve local files | `vol unmount DIR` | `unmount DIR` |
| Delete file tree | `vol delete --no-confirmation NAME` | `ws delete NAME --yes` |

The tests compare workspace names and checkpoint head availability, normalized directory entries, checkpoint file/folder/byte totals, and materialized paths, file sizes, SHA-256 byte digests, permission modes, and symlink targets. Timestamps, random identifiers, formatting, and private local control directories (`.afs-sync` / `.afs-lite-sync`) are excluded from equality. Ordinary hidden user files are included.

The shared fixture includes text, empty and binary files, an executable, an empty directory, a hidden file, and a relative symlink. Both CLIs must import and materialize that fixture, then publish ordinary local edits, rename, chmod, deletion, directory creation, and a 2,150,005-byte binary file. An independent remount verifies the published tree; the original fork must remain unchanged; restoring the earlier checkpoint must recover the original bytes and metadata. Unmount must preserve the local tree. Help and version run with a nonexistent config, without Redis.

## Intentional differences

- The reduced grammar and global JSON output replace the old command spelling and output schemas. Help moves from styled stderr to plain stdout. This is behavior compatibility for retained operations, not a drop-in command-line or configuration replacement.
- The original `fs cat` rejects binary content. The extracted command must return exact binary bytes; the suite explicitly tests both contracts.
- General `fs put`, `mkdir`, `mv`, `rm`, checkpoint deletion, and choosing an arbitrary checkpoint when forking have no corresponding command in the original CLI. Their direct command contracts are tested in `tests/e2e/cli_contract_test.go`. The compatibility suite exercises the shared mutation behavior through real folder mounts.
- Restore requires local mounts to be stopped and explicit confirmation. Unmount now flushes pending changes and fails if it cannot flush. The original workflow uses an explicit `vol save` before unmount/checkpoint.
- The old Redis config is an object and requires explicit `productMode: local`, `mode: sync`, and `runtime.mount.backend: none` for this test. The extracted config takes a Redis URL. The suite creates both configs and separate child homes/state directories; it never reads an installed AFS config or existing Redis endpoint.

## Run

Build the original binary from an archived original checkout, then invoke the comparison from this repository:

```sh
afs_baseline_dir=$(mktemp -d /tmp/afs-cli-baseline.XXXXXX)
git -C /path/to/agent-filesystem archive c3897ac05265444568a3819c21728a38a4ed254b | tar -x -C "$afs_baseline_dir"
(cd "$afs_baseline_dir" && go build -o afs-prior ./cmd/afs)
AFS_BASELINE_BINARY="$afs_baseline_dir/afs-prior" go test -tags compatibility -count=1 -v ./tests/compat
```

`make compat` runs the same comparison with `AFS_BASELINE_BINARY` set.

`redis-server` and Go are required. The suite builds the extracted CLI fresh by default; `AFS_E2E_BINARY` can select an already built derivative. Missing baseline binaries or Redis are failures, never skips. All spawned Redis servers and sync mounts are owned by the test and stopped during cleanup. No NFS/FUSE or account services are needed.

Observed on macOS arm64 with Go 1.26.1 and Redis 8.6.2: both top-level tests and all four subtests passed, with no skips. The paired filesystem workflow took 3.07 seconds in the recorded run; this is acceptance evidence, not a performance comparison. No production changes were needed to pass this comparison.

Final repeat with disposable Redis 7.0.15 after the empty-file deletion fix:
six test/subtest passes, zero skips; package time 3.856 seconds.
