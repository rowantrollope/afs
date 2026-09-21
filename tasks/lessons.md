# Lessons

## 2026-09-18 — Carry over the user-facing auth entry point

The user corrected the missing `afs auth` workflow after management and credential
bootstrap were restored. Restoring the underlying behavior is incomplete when
the original onboarding commands are missing. Inventory and execute the public
login, status and logout commands as part of connection-workflow acceptance;
make the UI and docs point to the same supported entry point.

## 2026-09-18 — Preserve control-plane connection setup and CLI management

The user requires the original credential distribution and control-plane-based
CLI management to carry over. A reporting-only URL does not preserve that
workflow. A configured control plane must support discovery, management and
Redis connection bootstrap; retain direct Redis file I/O and standalone use.
Keep one configured backend and the shared team token; do not infer a need to
restore Cloud, a multi-database catalog, or unrelated original product features.

- All new control-plane and UI implementation belongs in `/Users/rowantrollope/git/afs`; use `/Users/rowantrollope/git/agent-filesystem` only as read-only reference. Refresh current code before porting; `afs:` is already the canonical prefix.

## 2026-09-17 — Verify requested GitHub publication

The prefix correction was left uncommitted locally. When publication is requested,
commit the complete change, push to the intended branch, and verify the remote
commit before reporting completion. Integrate newer remote code and check that it
also follows the corrected namespace.

## 2026-09-17 — Keep the original Redis namespace

User explicitly corrected the derivative's Redis prefix: use `afs:`, not
`afs-lite:`. Retain local configuration/state paths unless separately requested.
Evaluate reuse of the original web UI with one workspace per tree; distinguish
its content-volume pages from the removed composed Agent Workspace model.
Shared key names alone do not prove safe interoperability with old writers.

## 2026-09-17 — HTTP serving belongs in the control plane

User rejected `history serve` as CLI bloat even under a single history group.
Keep the CLI focused on client actions; the control plane owns HTTP hosting and
server lifecycle. Retain reusable handlers and their compatibility tests without
adding a production server command merely to demonstrate the original web UI.
Use disposable test hosts for drawer validation until the separate control plane
is implemented. This correction supersedes the earlier optional CLI server.

## 2026-09-17 — Keep file history under one command group

User rejected adding `history`, `recover`, `versioning`, `file` and `serve` as
separate root commands, and selected a single `afs history` group.

- Preserve capabilities without duplicating command families for compatibility.
- Match the existing `cp` pattern: one group, focused subcommands, one listing
  format and useful subcommand help. Remove replaced roots rather than hiding
  aliases behind a smaller help screen.
- Preserve HTTP compatibility independently of CLI spelling; the latest user
  correction takes precedence over the earlier original-CLI compatibility goal.

## 2026-09-17 — Preserve the original versioning capabilities and interfaces

The user requires a capability superset, not a different reduced history feature.
Compare against the current original source and its web UI contracts. Preserve
those contracts unless there is a concrete cost that justifies adapting the UI.
Pair correctness tests with original-versus-new measurements before claiming an
improvement. The original already records native range writes; its separate
post-publication observer is the consistency distinction. The user explicitly
authorizes a commit and PR after full verification.

The user asks for an objective assessment of both strengths and weaknesses.
Separate measured local improvements from production maturity, memory costs,
write-availability tradeoffs and incomplete gates. A documented missing original
capability is still missing: audit ordinary sync attribution and worker restart
paths as well as explicit recovery APIs before claiming parity.

## 2026-09-16 — Describe automatic sync with an explicit wait boundary

User rejected a standalone save verb because it implies sync requires manual action.
Expose verified completion as `afs sync --wait` and observational progress as
`afs sync status`. Keep automatic synchronization explicit in help and docs; never
claim empty queues prove remote byte verification.

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

## 2026-09-21 — Keep navigation anchored to the viewport

- User correction: the left navigation must extend to the bottom of the browser.
- Give html/body/root a definite height; min-height alone does not resolve child percentage heights. Keep main-content scrolling inside the viewport-sized shell.
- When promoting a design preview to the real instance, inspect the active server checkout and preserve newer work there instead of replacing it with the preview backend.

## 2026-09-21 — Reuse opaque application cards on Home

- User correction: Home cards should match other pages and must not be transparent.
- Reuse SurfaceCard for Home panels and recipe cards rather than duplicating card chrome. Use the solid panel token for Classic, whose default panel token has alpha.
- Keep card surfaces opaque in light and dark modes so the background grid does not show through.

- Follow-up correction: the Learn Agent Filesystem hero is also a card. Use the same HomeCard surface for the hero, with no separate background or border; theme its text and avoid multiply blending on dark surfaces.

## 2026-09-21 — Home text must be comfortably readable

- User correction: enlarge all Home text, including labels, metadata, controls and the agent prompt. Do not retain tiny 8–11px editorial text; use 12px minimum for small labels, 16px body copy and larger headings, with responsive wrapping.
