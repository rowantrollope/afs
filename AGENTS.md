# AFS

This is the slim Go derivative of redis/agent-filesystem. Follow the current CLI in README.md and decisions in tasks/todo.md; tasks/brief.txt records the original extraction brief.

- Reuse the retained folder sync, Redis client, manifests, checkpoints, and recovery code.
- One workspace is one tree; no public volumes, cloud, MCP, or search. Native FUSE/NFS mounting is optional, with folder sync as the default.
- Workspace actions live at the root; checkpoint manages checkpoints, config manages settings, and sync provides status and an explicit verification wait. No ws/fs aliases. Access files through mounted directories.
- Never run tests against existing user Redis data or change the original installation.
- Retain multi-writer behavior and prove safety changes with focused regressions.
- Local lifecycle belongs in cmd/afs; Redis content and checkpoints remain internal.
- Keep tasks/todo.md current; record user corrections in tasks/lessons.md.
- Validate with build, vet, unit/race tests and isolated real-Redis process tests.
- Preserve scan batching, chunking, worker draining, warm recovery and directory-mode restoration.

- The optional control plane is `cmd/afs-control-plane`; do not add HTTP server commands to `afs`. Build embedded UI with `make control-plane`, not plain Go build.
- New implementation belongs here; the original `agent-filesystem` checkout is read-only reference. Keep one storage engine and direct Redis mounted I/O.
- Control-plane scope is self-managed Redis connections, a bootstrap team token, and named trusted-administrator API keys. Keys support expiry, usage tracking and API revocation; they do not enforce workspace isolation or revoke previously issued Redis credentials. The Databases tab can add and edit connections saved in a private SQLite metadata database, alongside administrator keys and default selection; reuse the same Redis storage engine per connection. The control plane starts without Redis and keeps metadata/auth independent of managed Redis availability. Postgres is deferred. No Cloud, search, hosted MCP, templates, or duplicated workspace catalog.

## Git workflow

- Everything stays on `main` unless the user explicitly directs otherwise: edits, commits, builds and running services. Do not create or use worktrees or feature branches.
- Run the control plane and development servers from `/Users/rowantrollope/git/afs`, never from a Codex worktree or its build artifacts.
- Keep the user's browser current after development changes: use the Vite UI on port 5173 for automatic UI updates, or run `make control-plane` and restart the embedded server. Go/backend changes always require rebuild/restart with the existing Redis/auth settings. Verify the active URL before reporting completion.
- Commit completed work and push it to `origin/main`; do not leave finished changes only in a local branch or worktree.
- Preserve existing work when integrating changes. Use normal pushes; never force-push `main`.
