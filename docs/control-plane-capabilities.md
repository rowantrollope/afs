# Control-plane capabilities and migration decisions

The original project was reviewed at `1ff1fa0`; the new AFS baseline was
`980c8ed`, already using `afs:`. This inventory describes source capabilities,
not a claim about which features are enabled in a particular deployment.
The implemented management layer lives entirely in this repository.

The control plane adds shared visibility and browser administration. File
synchronization, conflict handling, checkpoints, file activity and optional
per-file version capture already belong to the AFS engine and daemon.

| Original capability | What it contributes | New AFS decision |
| --- | --- | --- |
| Named active clients | Registration plus idle heartbeats; workspace, host, OS, version, path, labels, access mode and last seen | Keep for sync and native daemons |
| Live topology and client detail | Host grouping, connections to workspaces and client detail, using the session feed | Keep existing UI |
| Automatic attribution | Session ID links a daemon's file changes and captured versions to presence | Keep; reuse existing mutation metadata |
| Session lifecycle | Open/close activity and active/stale/closed state | Keep; presence is informational |
| Workspace/global History | Merges file changes, checkpoint/restore and session/lifecycle activity | Keep; read shared engine records |
| Live browser refresh | Monitor SSE invalidates affected UI queries | Keep, including changes from direct CLI/daemon writers |
| Session operation rollups | Counts, byte deltas and last operation per session | Omit separate rollup records/API; current UI has no consumer |
| Last-writer lookup | Dedicated per-path latest-writer index | Omit separate index/API; retain writer fields in file History |
| Workspace administration | List/detail/create/update/fork/delete and view live/checkpoint trees | Keep through the shared engine |
| Checkpoint administration | Create, compare, inspect and restore published snapshots | Keep; never imply a remote-daemon flush |
| File history and policy | Version list/content/diff, recovery and retention policy | Reuse the already retained engine/HTTP contracts and copied drawer |
| Redis monitoring | Health, version, memory, keys, operations, connected clients and workspace/session counts | Keep per configured self-managed connection |
| Managed connection setup | Original auth login/status/logout plus storage and credential discovery | Keep self-managed `afs auth login`, `status` and `logout`; authenticated bootstrap returns the selected backend's effective Redis credentials, used directly by mounts |
| CLI management through the API | Workspace discovery, lifecycle and checkpoint management without local Redis setup | Keep current workspace/checkpoint/history commands over HTTP, including streaming directory imports; explicit Redis operation remains available |
| Browser/API authentication | Original supported none, trusted-header and Clerk/Cloud identity | Bootstrap team token plus named administrator keys; loopback-only when the team token is unset |
| Scoped API-key management | Multiple key families, expiry, ownership and API capabilities | Keep named administrator-key issuance, list, expiry, usage and revoke in CLI/UI; store hashes and metadata in SQLite or Postgres independently of Redis. Omit workspace scopes and Cloud/MCP key families |
| Browser-to-CLI onboarding | One-use browser token exchange and setup | Omit browser exchange; use `afs auth login` with the server URL and optional shared team token |
| Multi-database administration | Profiles, credentials, defaults, cross-database views and catalog reconciliation | Add, edit, remove and select a default self-managed connection with private SQLite or shared Postgres persistence and scoped/global views; start with zero databases and retain administration during Redis outages. Omit Cloud provisioning and cached workspace/session catalog |
| Search | Ranked/semantic query, index status/build, embedding providers and model runtime | Omit backend and corresponding UI controls |
| Hosted MCP | Remote filesystem/checkpoint/query tools and scoped MCP keys | Omit |
| Templates | Static gallery plus installation through MCP and key issuance | Omit gallery and installation together |
| Quickstart | Starter workspace seeding and onboarding credentials | Omit automatic seeding; create/import with the existing CLI |
| Imports | Manifest/blob or server-local path import; not browser directory upload | `afs create --from` streams through the API in managed mode and retains direct batched imports in standalone mode |
| Cloud accounts and administration | Ownership, account reset/delete, users, quotas and cloud inventory | Omit |
| Utility/distribution endpoints | UI assets, version/health, download/install scripts | Keep embedded UI/version/health; use this repository's build/install workflow |
| Legacy models | Volumes/composition, retired routes, token formats and migration adapters | Omit; no backward-compatibility requirement |

## What client participation changes

The control plane now uses a small SQLite metadata store for connection profiles,
default selection and administrator keys. This replaces the local JSON registry
and the startup-Redis key registry; it does not restore the original platform's
workspace ownership catalog, cached discovery, onboarding tokens, scoped access
or account management. Hosted deployments can use Postgres for the same small
metadata model. Workspace contents and managed session presence
remain in Redis. Server readiness reports metadata availability independently
of Redis health, and CLI login verifies authentication without requesting Redis
credentials. See the [control-plane guide](control-plane.md) for the one-time
JSON import and explicit legacy API-key migration.

The server can discover existing workspaces and read file history directly from
Redis. A working browser does not prove that daemons participate in management.
Named clients require explicit registration and heartbeat, including while no
files change. Automatic attribution requires that session identity reaches the
same mutation context used by sync and native writers.

Managed CLI operations use HTTP; mount setup obtains Redis credentials and
workspace metadata from the server. Daemons register, heartbeat and close their
sessions. Mounted file bytes and filesystem operations continue directly between
the client and Redis. Server outages prevent managed commands and new mount
setup, but do not stop an already running mount's direct Redis connection.

## Limits inherited or deliberately retained

The old server did not demonstrate a general daemon-log collector, remote kill,
fleet-wide flush barrier, tamper-proof audit, or revocation of Redis credentials
on session close. These are not capabilities lost by slimming it down. Strong
per-client Redis ACL provisioning and revocation remain a separate design; see
the [earlier proposal](lightweight-control-plane.md), which is superseded for
this implementation's scope.

Client host/agent/user labels are provenance supplied by a client, not independent
proof of identity. API token holders are trusted administrators of all configured
backends. Per-file versions depend on the workspace capture policy; ordinary
file-change activity does not require retaining every historical file body.

## Why keep a control plane

A thin server provides the useful cross-machine view: who is connected, which
workspace they use, what changed, and browser administration. Removing it would
lose that view. Keeping the original platform wholesale would preserve unrelated
account, catalog, query and integration machinery. This implementation keeps the
working UI and management behavior around the new shared engine.
