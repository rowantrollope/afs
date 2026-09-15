# CLI compatibility with the original AFS

The compatibility suite runs two real CLI binaries against separate disposable Redis servers. It compares the extracted CLI with original commit `c3897ac05265444568a3819c21728a38a4ed254b`, using the original **volume** commands for the retained file-tree model. The original `ws` and top-level `mount` commands compose Agent Workspaces; those are different operations.

## Command mapping

| Retained operation | Original CLI | Extracted CLI |
| --- | --- | --- |
| Create empty file tree | `vol create NAME` | `ws create NAME` |
| Import directory | `vol import NAME DIR` | `ws create NAME --from DIR` |
| List / inspect | `vol list` / `vol info NAME` | `ws list` / `ws info NAME` |
| List directory | `fs NAME ls PATH` | `fs ls NAME PATH` |
| Read text / empty file | `fs NAME cat PATH` | `fs cat NAME PATH` |
| Fork checkpoint head | `vol fork SOURCE NEW` | `ws fork SOURCE NEW` |
| Create named checkpoint | `cp create --volume NAME LABEL` | `cp create NAME --name LABEL` |
| List / inspect checkpoint | `cp list NAME` / `cp show NAME LABEL` | `cp list NAME` / `cp show NAME LABEL` |
| Restore checkpoint | `cp restore NAME LABEL` | `cp restore NAME LABEL --yes` |
| Sync ordinary local folder | `vol mount NAME DIR` | `mount NAME DIR` |
| Flush before checkpoint | `vol save --json DIR`, then `cp create` | `cp create` flushes registered local mounts |
| Stop sync, preserve local files | `vol unmount DIR` | `unmount DIR` |
| Delete file tree | `vol delete --no-confirmation NAME` | `ws delete NAME --yes` |

The tests compare workspace names and checkpoint head availability, normalized directory entries, checkpoint file/folder/byte totals, and materialized paths, file sizes, SHA-256 byte digests, permission modes, and symlink targets. The machine comparisons request JSON explicitly. Timestamps, random identifiers, exact whitespace, and private local control directories (`.afs-sync` / `.afs-lite-sync`) are excluded from equality. Ordinary hidden user files are included.

The shared fixture includes text, empty and binary files, an executable, an empty directory, a hidden file, and a relative symlink. Both CLIs must import and materialize that fixture, then publish ordinary local edits, rename, chmod, deletion, directory creation, and a 2,150,005-byte binary file. An independent remount verifies the published tree; the original fork must remain unchanged; restoring the earlier checkpoint must recover the original bytes and metadata. Unmount must preserve the local tree. A separate paired test starts with a local `.afsignore` containing `private/` and an existing `private/local.txt`, then mounts a workspace containing `public.txt`. Both CLIs must preserve the ignore file and private bytes, hydrate the public file, exclude the private paths through a completed save/checkpoint, preserve them on unmount, and expose only public content on an independent mount. Help and version run with a nonexistent config, without Redis.

## Default presentation

Both CLIs default to readable output: tables for lists, labeled workspace and checkpoint details, short mutation confirmations, and clear empty-state messages. `TestDefaultPresentationMatchesPrior` runs the actual binaries and checks names, list headings, file type/size, checkpoint counts, mount/status information, and success messages without pinning whitespace alignment. The original passes; the saved derivative from before the presentation fix fails because its defaults are JSON.

JSON is opt-in through `--json`. Existing machine comparisons and parsing helpers pass that flag explicitly. The presentation test also verifies JSON schemas remain available and `fs cat` preserves exact bytes even when the file itself contains JSON.

## Intentional differences

- The reduced grammar and opt-in global `--json` flag replace the old command spelling and output schemas. Help moves from styled stderr to plain stdout. This is behavior compatibility for retained operations, not a drop-in command-line or configuration replacement.
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

## Recorded result

The final suite passed on macOS arm64, Go 1.26.1 and disposable Redis 7.0.15:
four top-level tests and eight subtests, zero skips or failures, 8.283 seconds.
The original binary was built from the exact baseline commit above; the
extracted binary was built fresh from the tested source.

The ignored-file regression showed that initial bulk hydration removed
pre-existing ignored local files. The corrected predicate uses
the retained warm reconciliation path whenever user-owned local entries exist.
The final comparison passes after that fix and the deletion/retry regressions
recorded in [the simplification report](simplification.md).

The default-presentation comparison also reproduced the JSON-default regression:
the original passed and the preserved pre-fix derivative failed. The freshly
built derivative now passes the readable-default assertions and the explicit
JSON and exact-byte checks together.
