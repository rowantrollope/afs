# Lessons

## 2026-09-22 — Name the agents button

- User correction: the header help button must visibly say `agents >`, with its blinking cursor to the right of the label.

## 2026-09-22 — Unmount accepts workspace identity

- The user expects `afs unmount` to accept a workspace name when it identifies one local mount; require the directory only when the selector is ambiguous.
- Root and command help must advertise both workspace names and local directories. Resolve names and paths together so collisions never select the wrong mount, and preserve the normal flush and force-detach behavior.

## 2026-09-22 — Configurable mount identity

- Align topology details around the middle of the node: labels right-aligned, values left-aligned in equal columns below the title.
- Default Live Topology to Agent ID and mount path only: the default agent name duplicates the system name already shown in the host group. Uptime belongs in the optional detail list; showing every available tag by default is too busy.
- The control-plane UI should display the informational metadata supplied to `afs mount`, including session labels, agent IDs, users, labels and versions, rather than only a generated session ID.
- The user wants a Config button on Live Topology to choose the node label and visible details. Persist those preferences in the browser, handle missing fields gracefully and keep full metadata available in session details.

## 2026-09-22 — Live Topology shows current mounts

- The user expects unmounted sessions to disappear from Live Topology, rather than remain as blue inactive nodes.
- Session listings retain closed/stale history; filter live mount states before deriving topology nodes, workspace connections and Monitor counts. Keep idle mounts with a valid session visible and preserve historical records.

## 2026-09-22 — Keep development changes visible

- The user reaffirmed that no worktrees should be used: edits, commits, builds and running services all belong on `main` in `/Users/rowantrollope/git/afs`.
- The user accepts either `npm run dev` for live UI updates or rebuilding/restarting the control plane after development changes. Use Vite on `http://127.0.0.1:5173` from the main checkout for UI work; port 8091 serves embedded assets and does not hot reload.
- Verify the actual Vite process cwd as well as the API process: an old worktree's dev server can keep serving stale code even when the backend runs from main.
- After Go/backend changes, run `make control-plane` and restart the active server with its existing Redis/auth settings. Do the same for UI changes when using the embedded UI, then refresh and verify the active browser URL.

## 2026-09-21 — Home recipes need complete linked content

- Migrate the actual upstream templates into usable recipe guides; generic command drawers do not replace the requested content. Verify every card, direct URL, starter-file preview and setup prompt end to end.
- Use the user's clarified source, https://github.com/redis/agent-filesystem, rather than assuming a similarly named local checkout is current.

## 2026-09-21 — Availability does not establish build freshness

- When the user asks for the new control plane, a healthy existing server is not completion. Rebuild the latest main checkout, restart the actual listener with its existing settings, and compare served assets with the new build.
- Verify local HTTP outside the network sandbox before reporting an endpoint unavailable. Check Redis configuration before giving a localhost example as the user's startup command.

## 2026-09-21 — Work directly on main

- User instruction: all project work belongs on `main`, without new branches, and completed changes must be pushed to GitHub `origin/main`.

## 2026-09-21 — Verify sidebar height in the browser

The user reported that the sidebar stopped before the bottom of the browser.
Percentage heights need definite heights through every ancestor; `min-height`
alone does not establish that chain. Check short and overflowing pages at multiple
viewport sizes, with the sidebar expanded and collapsed.

The user then reported no visible change because only a separate worktree build
had been verified. For a reported local UI bug, identify the process serving the
user's URL, update its actual checkout and embedded assets, and restart it while
preserving its configuration. Reload and inspect the user's browser before
reporting the visible issue fixed.
## 2026-09-21 — Verify sidebar height in the active version

The user reported that the full-height sidebar fix had not carried into this
version. Verify the rendered layout at the active URL after rebuilding. A chain
of `height: 100%` requires a definite root height; `min-height` alone does not
provide one. Keep the sidebar viewport-height while the main panel scrolls,
and check both expanded and collapsed navigation.

## 2026-09-21 — Keep the user's active UI current

The user expected UI changes to appear in the browser and asked for the running
control plane to be restarted. Distinguish the live process from the edited
worktree. An embedded production build needs rebuild/restart plus page refresh;
use the existing Vite development server for automatic UI updates during edits.
When authorized to restart, preserve the actual process's Redis/auth settings
and verify the user's active URL, rather than stopping at an isolated QA build.

## 2026-09-21 — Restore adding self-managed Redis databases

The user explicitly requested Add database on the Databases tab. This supersedes
the earlier single-backend UI restriction. Restore the complete usable flow:
validated connection entry, durable private settings, database selection for
workspace creation, scoped data and correct CLI connection commands. Continue
using the retained AFS engine without restoring Cloud or a SQL catalog.
## 2026-09-21 — Spell out checkpoint commands

The user replaced `afs cp` with `afs checkpoint`: public command groups should
use full names, and `cp` already means Unix copy. This supersedes earlier
instructions to retain `cp`. Update dispatch, help, errors, examples and test
callers together; do not keep an abbreviated compatibility alias.

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
## 2026-09-21 — Promote designs without regressing the active instance

- User asked to run the Home design in the real instance and keep navigation full-height.
- Identify the running server checkout before restarting. Merge UI work into that checkout, preserving concurrent features and saved connection settings.
- Verify actual sidebar/viewport bounds and independent content scrolling; a full-page screenshot alone can miss the shell-height defect.

## 2026-09-21 — Reuse opaque application cards on Home

- User correction: Home cards should match other pages and must not be transparent.
- Reuse SurfaceCard for Home panels and recipe cards rather than duplicating card chrome. Use the solid panel token for Classic, whose default panel token has alpha.
- Keep card surfaces opaque in light and dark modes so the background grid does not show through.

- Follow-up correction: the Learn Agent Filesystem hero is also a card. Use the same HomeCard surface for the hero, with no separate background or border; theme its text and avoid multiply blending on dark surfaces.

## 2026-09-21 — Home text must be comfortably readable

- User correction: enlarge all Home text, including labels, metadata, controls and the agent prompt. Do not retain tiny 8–11px editorial text; use 12px minimum for small labels, 16px body copy and larger headings, with responsive wrapping.

## API-key scope correction — 2026-09-21

The user approved bringing named administrator API keys into the derivative,
superseding the earlier omission of API-key issuance/UI. Keep the migration
focused: creation/list/revoke, expiry, last use, hashed secrets and key attribution.
All keys are trusted administrators; workspace isolation and revoking existing
Redis access require separate storage authorization work. Retain the shared
team token for bootstrap/recovery and keep the original checkout read-only.

## No more worktrees, including running services — 2026-09-21

The user reiterated: “NO MORE WORKTREES” and “Everything stays on main unless I direct otherwise.” This applies to service launch paths
as well as source edits and Git workflow. Build and run AFS from the main
checkout at `/Users/rowantrollope/git/afs`. When restarting an inherited server,
verify its executable and working directory and replace an old worktree launch
with the main checkout while preserving its existing configuration. Do not
delete existing worktrees or their uncommitted work without a separate request.

## Contextual Agent help — 2026-09-22

- User correction: the header terminal icon is the “Agent” button, and its panel must provide relevant help on every page, including suggested agent prompts and AFS CLI guidance.
- Cover nested workspace tabs, History views, and individual recipes as well as top-level navigation. Keep workspace examples tied to the actual database, and distinguish snapshot browsing from the live mount.
- Copy prompt prose separately from commands, preserve exact command text, and explain UI-only actions without inventing CLI commands.
