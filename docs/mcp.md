# Optional MCP file access

`cmd/afs-mcp` is a separate adapter built with the
[official MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk), pinned to
v1.8.0 (Go 1.25+). It serves exactly one existing writable folder-sync mount.
The server operator runs AFS; agents receive file tools, never Redis connection
details or AFS administrator credentials. There is no workspace selector in
tool calls, account system, OAuth, cloud provisioning, search, checkpoint tool,
template system or workspace administration API.

## Server-side setup

The simplest supported lifecycle is two supervised processes: an ordinary AFS
folder-sync mount and the adapter attached to that mount. Both run as the same
trusted OS user. Use a dedicated mount directory without `.afsignore`, and the
same explicit config and state directory in both processes. Keep configuration
and state outside the shared folder. The examples use `/srv/agentconnect`; use
absolute paths appropriate for your server. Configure Redis only on the server.

```sh
make mcp
mkdir -p /srv/agentconnect/state /srv/agentconnect/seed
chmod 700 /srv/agentconnect/state
printf '# Shared folder\n' > /srv/agentconnect/seed/readme.md

# Private server config; this creates it without changing user defaults.
./bin/afs --config /srv/agentconnect/afs.json config set redis \
  'redis://127.0.0.1:6379/0'

AFS_STATE_DIR=/srv/agentconnect/state ./bin/afs \
  --config /srv/agentconnect/afs.json create shared \
  --from /srv/agentconnect/seed

# Process 1: supervise this foreground folder-sync mount.
AFS_STATE_DIR=/srv/agentconnect/state ./bin/afs \
  --config /srv/agentconnect/afs.json mount shared \
  /srv/agentconnect/folder --foreground
```

In another terminal or supervisor unit, start process 2:

```sh
# Generate once and save privately in your supervisor's secret environment.
export AFS_MCP_TOKEN="$(openssl rand -hex 32)"

./bin/afs-mcp --transport http --listen 127.0.0.1:8092 \
  --workspace shared --directory /srv/agentconnect/folder \
  --afs-bin /absolute/path/to/afs/bin/afs \
  --config /srv/agentconnect/afs.json --state-dir /srv/agentconnect/state
```

Redis and both binaries use the existing AFS storage engine. `afs-mcp` calls
`afs sync status` and `afs sync --wait`; it does not own, create, select or
unmount workspaces. Startup verifies publication before serving MCP. It fails
if the mount is stopped, disconnected, mismatched, read-only, has sync errors,
has `.afsignore`, or cannot be opened. Directory configuration is canonicalized
to the mount registry's path. Each tool checks the live daemon, Redis root,
directory identity and permissions again; `workspace_status` reports
`not_ready` when these checks fail. Status is a health observation, not a
synchronization barrier. Healthy status does not guarantee every file is a
supported UTF-8 file or that arbitrary subdirectories are writable.

The HTTP listener accepts only numeric loopback addresses (`127.0.0.1` or
`[::1]`). `/mcp` uses Streamable HTTP with stateless JSON responses. Every HTTP
method requires `Authorization: Bearer <AFS_MCP_TOKEN>`; missing/wrong tokens
return 401. Tokens must have at least 32 non-whitespace bytes. Host validation
and same-origin checks protect local browser access. No CORS permissions are
granted. JSON requests/stdio frames are bounded to 8 MiB so escaped 1 MiB text
fits. `--timeout` bounds each AFS verification (default 30s, range 1s–5m).

## AgentConnect integration

AgentConnect can run this adapter server-side for each shared folder. Its backend
connects to loopback `/mcp` with the private token, or owns a stdio child. A cloud
agent cannot reach a server's loopback listener directly. AgentConnect must
provide its own authenticated HTTPS route/broker to reach this adapter; that
public route, account isolation and runtime registration are outside this
feature. When proxying HTTP, set the upstream Host to the loopback authority,
strip external browser Origin after your own authentication/origin validation,
and keep the upstream token private. Do not expose an unauthenticated proxy.

For a client that runs on the server (or through an authenticated tunnel):

```json
{
  "mcpServers": {
    "shared-files": {
      "url": "http://127.0.0.1:8092/mcp",
      "headers": { "Authorization": "Bearer ${AFS_MCP_TOKEN}" }
    }
  }
}
```

For a server-side stdio child, omit `--transport` (stdio is the default):

```json
{
  "mcpServers": {
    "shared-files": {
      "command": "/absolute/path/to/afs/bin/afs-mcp",
      "args": [
        "--workspace", "shared", "--directory", "/srv/agentconnect/folder",
        "--afs-bin", "/absolute/path/to/afs/bin/afs",
        "--config", "/srv/agentconnect/afs.json",
        "--state-dir", "/srv/agentconnect/state"
      ]
    }
  }
}
```

Stdio access relies on OS process permissions; it needs no connection token.
Stdout contains only SDK MCP framing. Diagnostics go to stderr. Multiple stdio
adapter processes can share the same mount; HTTP supports independent clients.
All clients of an adapter share its one workspace and write privilege.

## Tool contract

| Tool | Arguments | Result |
| --- | --- | --- |
| `list_files` | Optional `path` directory; empty or `.` is root | Sorted immediate `files` entries with relative `path`, `type` (`file`/`directory`), `bytes`; at most 1000 directory entries, no recursion |
| `read_file` | Required `path` | `content`, `bytes`, lowercase hex `sha256` |
| `write_file` | Required `path`, `content`, `sha256` | `bytes`, `sha256`, `created`, `verified: true` after AFS verification |
| `workspace_status` | None | `workspace`, `ready: true` on current health check |

Successful results include `workspace`, `ready` and `verified`. `verified` is
true only for successful writes. Read/list/status do not force publication.
Every result is returned as structured content and JSON text, with an inferred
output schema. File operation errors set MCP `isError: true` and return
`error: {code, message, retryable}`, `ready: false`, `verified: false`.
SDK schema/unknown-tool failures retain SDK protocol behavior.

Paths use relative slash-separated components. Absolute paths, `..`, embedded
`.` components, empty components, backslashes, colons, invalid UTF-8 and NULs
are rejected. Each path component is opened relative to a pinned directory
descriptor with `O_NOFOLLOW`; symlinks are rejected even inside the workspace.
Hard links and special files are rejected. AFS control names (case-insensitive
`.afs*` and `.afssync.*`), `.DS_Store` and `._*` names are blocked/omitted.
Listings include metadata for regular files beyond the read limit or containing
non-UTF-8 bytes; `read_file` returns a specific unsupported/size error.

Writes support at most 1,048,576 bytes, measured as UTF-8 bytes rather than
characters. Empty files are allowed. Compute the SHA-256 of the exact bytes,
including newlines; send 64 lowercase hex characters. The adapter creates
missing parent directories and links a completely written, fsynced staging file
into place atomically without replacement. Existing identical content succeeds
with `created: false`; different content returns `already_exists`. The adapter
has no overwrite/delete/rename tool. Immutability applies to MCP writes; trusted
local or other AFS writers can still change these paths. Use new unique paths
for revised reports and treat a SHA-256 plus relative path as a content reference,
not a historical object-storage URL. Re-read and compare the hash before use.

Successful `write_file` runs the existing AFS verification barrier and rechecks
the local content hash. Verification acknowledges Redis live-tree visibility,
not Redis disk persistence or delivery to another mount. Read/list observe the
local synchronized folder and may lag other mounts. Keep non-MCP writers from
changing the tree during a barrier; AFS's existing multiwriter conflict rules
remain active. Verification can also publish other pending folder changes,
including deletions, so the write tool conservatively advertises
`destructiveHint: true`, `readOnlyHint: false`, `idempotentHint: true`.
Read/list/status have `readOnlyHint: true`. All tools have `openWorldHint: false`.

On `publication_unconfirmed`, retry the **same path/content/hash**. The file
may already exist locally or in Redis; an identical retry re-verifies it without
rewriting. `not_ready` requires operator recovery before retry. Other codes
include `invalid_path`, `hash_mismatch`, `invalid_content`, `unsafe_path`,
`not_found`, `unsupported_file`, `file_too_large`, `directory_too_large`,
`already_exists`, `file_error` and `write_failed`. Generic OS/lifecycle errors
are sanitized so agents never receive server paths or connection credentials.

## Stop, recovery and security boundary

Stop the adapter before stopping the mount. EOF ends stdio; SIGTERM/interrupt
gracefully stops HTTP. Then run the ordinary `afs unmount` with the same config
and `AFS_STATE_DIR` to flush. Stopping the adapter does not detach the mount.
After a crash, remount the same workspace/directory using the retained private
AFS state, wait for mount readiness, and restart the adapter. The normal AFS
warm-recovery logic applies. Startup/identical retry remove only recognized
orphan MCP staging names under `.afs-lite-sync` while holding the writer lock.
A process-local gate and cross-process file lock serialize MCP writes; existing
AFS batching, chunking, draining, manifests and directory modes are untouched.

Only trusted administrators/local OS processes may manipulate the mount root,
parents, control directory, config or state. The adapter is a remote file-tool
boundary, not a sandbox against a malicious server OS user or Redis writer.
Use one dedicated writable mount per adapter workspace; do not nest mounts or
place operator secrets there. Do not add/change `.afsignore` for the mount's
lifetime: the daemon retains ignore rules loaded at startup. Remount without
custom ignores before attaching the adapter.

Rotate `AFS_MCP_TOKEN` by restarting the HTTP adapter and updating its caller.
This stops access through that adapter token. It does not revoke Redis
credentials or access through other AFS mounts. Existing control-plane keys
grant trusted-administrator access, do not provide workspace isolation, and
cannot revoke Redis credentials already issued. This adapter's token is
separate from those keys and is never accepted by the control plane.

## Validation

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...
go test -tags=integration -timeout=15m -count=1 -v ./tests/e2e ./internal/filehistory
```

`TestMCPIndependentClientsAndRecovery` owns a disposable Redis process and
private AFS config/state. Both stdio (two independent adapter processes) and
HTTP (two independently initialized SDK clients) prove initialization/tool
discovery, listing, reading `readme.md`, two-direction write/read, exclusive
create races, identical retries, conflicts, path/token isolation, outage
readiness, Redis/daemon/adapter restart and hydration of a new mount. Unit/race
regressions cover UTF-8/size/hash limits, symlinks/hard links/control files,
structured errors, annotations and recovery after link-before-unlink crashes.
No test uses existing user Redis data or settings.
