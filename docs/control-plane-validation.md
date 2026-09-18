# Control-plane migration validation

## Managed CLI follow-up — 2026-09-18

The CLI now uses the configured control plane for workspace, checkpoint and
history management, including streamed directory imports. Sync and native
mounts bootstrap the server's Redis connection without saving those credentials
in user configuration. Standalone operation remains available.

Disposable process acceptance covers:

- A fresh config containing only the HTTP URL and team token; workspace CRUD,
  fork, checkpoint create/list/show/restore/delete and history policy/export.
- Directory imports preserving binary contents, modes, symlinks and empty files.
- A password-protected Redis backend with URL-special password characters;
  stale client Redis settings/password environment do not override the server.
- Local checkpoint flush, cross-mode mount guards and private native bootstrap.
- Existing mount read/write, verified sync and unmount during server outage;
  new managed commands and mount setup fail without falling back to another Redis.
- Credentials absent from saved config, mount registry, persistent daemon state,
  logs and status; explicit `--redis` still selects standalone operation.
- Authenticated, uncached credential delivery; redirects refused; invalid and
  incomplete streamed imports remain unpublished.
- Progress-based idle deadlines for stalled uploads and HTTP/TLS responses;
  active transfers can exceed the interval. Failed imports release their name
  locks, including early rejection and incomplete HTTP epilogues.

Final automated checks: CLI and embedded UI/server build and Go vet pass. Full
unit and race suites each pass 1,004 cases with four optional Array skips. All
206 isolated Redis process cases pass. Logs are retained at
`/private/tmp/afs-managed-{unit,race,integration}-final.jsonl`.

The earlier browser acceptance below covers the retained UI. This follow-up
updates connection instructions and verifies the UI build, 41 tests and lint.
It does not rerun the Linux kernel FUSE/NFS fleet matrix or optional Redis Array
acceptance. All automated Redis work uses disposable instances.

## Original control-plane acceptance — 2026-09-17

Validated 2026-09-17 on macOS with Redis 8.6.2. Baseline: AFS `980c8ed` plus the
uncommitted control-plane/UI implementation. Original reference: `1ff1fa0`.
All Redis instances, CLI configurations, mount roots, sessions and browser data
used for acceptance were disposable. The original installation was not changed.

## Automated checks

| Check | Result |
| --- | --- |
| `go build ./...`, `go vet ./...` | Pass |
| `go test ./...` | 975 passing cases; four optional Array-backend skips |
| `go test -race ./...` | 975 passing cases; same four optional skips |
| `go test -tags=integration -timeout=15m -count=1 ./tests/e2e ./internal/filehistory` | 201 passing cases; no skips |
| Clean UI dependency install | `npm ci --userconfig /dev/null` succeeds against the public registry |
| UI build, tests and lint | TypeScript + Vite build; 41 tests in 13 files; lint passes |
| Native managed-session regression | Actual NFS RPC writes preserve attribution; close and startup failure clean up presence |
| Private bootstrap regression | API token excluded from argv, child environment and mount registry; bootstrap mode 0600 |
| HTTP/auth regressions | Token enforcement, browser origins, loopback Host guard, wrong-storage proof, unverified expiry, retired generation and idempotent closure |
| History/checkpoint regressions | Exact version identifiers, sparse filtered pagination beyond 10,000 events, mutation-journal guarded checkpoint commit and missing-root recovery |

The real-process management test starts the separate server and a sync daemon,
checks an idle heartbeat, publishes attributed changes, receives monitor SSE,
creates/browses a checkpoint, stops the HTTP server while sync continues, restarts
it, verifies the same session identity after worker restart and closes the session
on unmount. This proves separation of management and mounted file traffic.

The optional Redis Array skips do not cover that backend. Native management was
exercised through real NFS protocol operations and the retained unit/race suites;
this task did not rerun the Linux kernel FUSE/NFS fleet matrix.

## Browser acceptance

The embedded production UI was opened against an isolated authenticated server.
The existing visual layout/components were retained. Verified:

- Bearer-token sign-in and authenticated navigation.
- Monitor shows the actual managed sync client, its host/workspace connection,
  version, access mode, local path, IDs and heartbeat times.
- Workspace browser shows current file bytes. File History selects distinct
  earlier versions and renders a nonempty content diff.
- Browser-created checkpoints preserve the supplied name, description and actor;
  comparison reports the correct changed path and byte delta.
- Workspace History shows file and checkpoint events; its Sessions filter shows
  registration/start events with the matching agent.
- Workspace metadata updates, versioning settings and single-backend Redis
  statistics load successfully.
- Creating an empty workspace through the UI adds it to the same workspace list.
- A missing live root leaves healthy workspaces visible, offers saved checkpoint
  browsing, and is recoverable by restoring its current checkpoint through the UI.
- File activity links to its exact retained version; browser diagnostics are clean.
- A file written through the managed daemon appears in the already-open browser
  without a reload; a newly dirty tree changes the available browser views.

Local logs: `/private/tmp/afs-control-plane-unit-final.jsonl`,
`/private/tmp/afs-control-plane-race-final.jsonl`,
`/private/tmp/afs-control-plane-integration-final.jsonl`.
All disposable acceptance services and the temporary browser tab were shut down.
Changes remain uncommitted; no installed binary or user configuration was changed.
