# Lessons

## 2026-09-16 — Capitalize REDIS display labels

Use `REDIS:` in CLI headers and detail labels, including `Configured REDIS:`.

## 2026-09-16 — Credentials remain optional in JSON

Keep URL passwords supported; provide an environment override rather than
forbidding saved credentials. Add no dependencies for this feature. Apply the
override at client construction so it never enters serialized bootstrap/config.
Provide AFS_REDIS_URL alongside AFS_REDIS_PASSWORD for consistent environment
configuration, with an explicit --redis taking precedence over the URL variable.

## 2026-09-16 — Display date before 12-hour time

User's final preference supersedes the earlier time-first request: put date first,
`dd/mm/yyyy hh:mm:ss AM/PM`, in the system local timezone. Use AM/PM,
not 24-hour time. Keep all display paths
on the shared formatter and document this ordering consistently.

## 2026-09-16 — Installation must upgrade earlier AFS binaries

An earlier installation used a regular executable, not a checkout symlink.
`make install` must recognize and upgrade that AFS binary, as well as handle
fresh installs and repeats. Verify the actual existing installation shape;
do not assume it already matches the new installer. Preserve unrelated paths
and existing user configuration.

## 2026-09-16 — Work on main; do not accumulate task branches

User does not want branches and requested a review of all existing branches.

- Use main for future work unless the user explicitly requests a branch.
- Before branch cleanup, inspect live remote refs, merge ancestry, unique work,
  linked worktrees and uncommitted files; preserve work that is not on main.
- Distinguish a branch review from deletion of branches or dirty worktrees.

## 2026-09-16 — Apply the user's replacement database to both test hosts

When the user supplies a replacement Redis URL for a rerun, use it on both
systems through private test-only configuration. Keep their saved defaults
unchanged, exclude credentials from reports, and retain the same source and
workload when comparing results with the previous database.

## 2026-09-16 — Execute the complete two-host acceptance run

When asked to run end to end on Sancho and the Mac, transfer the current source,
build and execute on both hosts using their configured Redis, then collect and
evaluate the results. Do not stop at local-only validation or hand back setup
instructions when SSH access is available and execution is authorized.


## 2026-09-16 — Two-system testing uses the user's remote Redis

The user already has a remote Redis server and wants both systems to use it.
Do not require another Redis deployment. Isolate the workload in fresh, uniquely
named workspaces and private local state; never flush/restart the shared server
or modify pre-existing workspaces. Validate the harness itself with disposable
local Redis, without connecting to the user's server.

## 2026-09-16 — Align CLI help descriptions

Keep command and option descriptions in one consistent column; check rendered
help after changing command names or padding.

## 2026-09-16 — Keep installation in Makefile

User clarified that CLI installation should live directly in `make install`,
without a separate installer script. Keep this simple workflow in the Makefile.

## 2026-09-16 — Keep Redis connection failures concise

User requested only the final `afs: connect to Redis ...` error when Redis is
unreachable. Suppress library retry diagnostics; preserve the CLI error and
nonzero exit status. Verify this through the built executable.
Follow-up: use “Cannot connect to Redis on [url]” with a concrete --redis command
example, omitting raw dial errors and credentials.

## 2026-09-16 — Honor an explicit publication target

User requested the design Markdown directly on main, correcting a feature-branch
suggestion.

- Commit documentation to main when the user explicitly requests it; do not
  substitute a feature branch or pull request.
- If another task owns the checkout, use an isolated checkout of live main.
  Stage only the requested documentation and required bookkeeping, then use a
  normal push and verify the remote file.

## 2026-09-14 — CLI behavior is the acceptance boundary

User clarified that completion is evaluated through the resulting `afs` command
line against the prior CLI, including whether the reduced instructions work.

- Start acceptance from documented commands and observable results, not only
  retained package tests or implementation-level safety checks.
- Maintain an explicit old-to-new command mapping. Execute both versions against
  disposable infrastructure when an equivalent exists.
- Test every reduced command and README workflow through the built binary;
  distinguish intentional surface changes from behavioral regressions.
- Do not call the extraction complete until the resulting CLI contract passes.

- Compare initial mount and remount separately. Include pre-existing ignored
  files: an entry excluded from sync must not be treated as disposable data.

## 2026-09-14 — Preserve default terminal output

User noticed that structured CLI responses became JSON by default.

- An explicit `--json` option must select machine-readable output; it must not
  merely change JSON indentation unless that contract is explicitly requested.
- Preserve readable default output when simplifying command wiring. Test both
  default terminal presentation and explicit JSON, not just normalized results.

## 2026-09-14 — Keep the CLI centered on workspace lifecycle

- File access belongs to ordinary mounted directories; remove the public `fs`
  group without deleting the shared native client needed by synchronization.
- Workspace actions live at the root. Keep `cp` as the separate checkpoint
  group so create/delete operations have an unambiguous object.
- When removing commands, move behavioral acceptance to the retained public
  workflow rather than silently dropping synchronization or byte/metadata checks.

## 2026-09-14 — Native mounting needs kernel acceptance

User required implementation to continue until the concurrent tests pass.

- Reuse the core's process lab with actual FUSE/NFS mounts and mixed sync peers.
  Compilation and adapter mocks do not exercise kernel caches or mount cleanup.
- Preserve independent byte and permission oracles through a fresh observer.
  Diagnose failing assertions and add regressions before changing production code.
- Bound the whole native run outside the workload process; a blocked filesystem
  call can prevent an ordinary in-process timeout from firing.
- Freeze production before the final full runs, and report the tested binary
  hashes, cleanup outcome and platform limits.
- Serialize macOS native mount lifecycle tests and suites using system-wide
  open-file scans. An unrelated mount detaching during `lsof` can make that
  scan uncertain; preserve the refusal and test the suites in isolation.
