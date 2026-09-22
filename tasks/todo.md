# AFS extraction

## Unmount by workspace name or directory — 2026-09-22

- [x] Reuse shared local mount resolution for unmount, rejecting ambiguous names before lifecycle actions.
- [x] Document workspace-name and directory forms in root help, command help and README.
- [x] Cover unique names, path/name collisions, multiple databases, directory aliases, native routing and failed-unmount preservation.
- [x] Validate build, vet, unit/race and isolated real-Redis process tests; rebuild the CLI and restart the control plane.
- [x] Commit and push main.

Review: `go build ./...`, `make build`, `go vet ./...`, `go test ./...` and
`go test -race ./...` pass. Focused race tests also pass with caching disabled.
Fresh-binary process tests verify name-based flush/local preservation, ambiguity
with and without force, directory disambiguation, existing sync verification and
read-only unmount. All process tests use disposable Redis and client state.
`bin/afs unmount --help` advertises `<workspace|directory>` and ambiguity guidance.
`make control-plane` passed; replacement API PID 7418 runs from main with the
previous Redis/auth settings preserved. Health and both UI endpoints pass; the
browser's active AFS URL is `http://127.0.0.1:5173/`. Concurrent UI work is preserved.

## Configurable mount metadata in Live Topology — 2026-09-22

- [x] Trace mount flags through managed sessions and retain the missing user field in the UI.
- [x] Prefer supplied session/name/label/agent identity over generated session IDs; expose all supplied metadata in details and search.
- [x] Add Live Topology Config for node labels and optional details, persisted in browser storage with reset defaults.
- [x] Cover metadata fallbacks, API mapping, search, configuration, persistence and accessibility with focused UI regressions.
- [x] Complete UI/build/browser verification and record results.
- [x] Commit and push main.

Scope: frontend metadata presentation only. Reuse the existing session API;
`--session` is a descriptive session name, `--label` also populates agent name,
and `--agent-version` is returned as `afs_version`. Keep generated session IDs
available in details and as a fallback. Preserve concurrent unmount work.

Review: all 130 UI tests, TypeScript, lint, `make control-plane`, Go build/vet/
unit/race and a fresh isolated `TestControlPlaneManagedSyncLifecycle` pass.
The process test uses disposable Redis and strips inherited AFS settings.
Browser verification at `http://127.0.0.1:5173/monitor` confirmed live hot updates,
Config label selection, detail toggles, persistence after reload, reset defaults
and the full metadata dialog. Default Config is open in Safari. Independent
review preserved the Monitor's combined names and added accessible node details.
Vite continues from the main checkout; no backend changes or restart required.
Logs: `/private/tmp/afs-mount-metadata-{go-build,go-vet,go-test,go-race,process-test,embedded-build}.log`.

## Remove unmounted sessions from Live Topology — 2026-09-22

- [x] Trace retained session history and identify connected mount states.
- [x] Filter topology nodes, connection counts and Monitor active-agent rows to live sessions.
- [x] Verify close/stale transitions and reconnection regressions, UI checks, build/vet/unit/race and isolated real-Redis lifecycle tests.
- [x] Verify the updated active browser URL, commit and push main.

Scope: closed/stale sessions remain in history, but no longer appear as connected
mounts. Starting, active (including idle) and supported syncing sessions remain
visible. Preserve workspace catalog nodes and direct Redis mount behavior.

Review: all 102 UI tests, UI lint, `make control-plane`, Go build/vet/unit/race,
and `TestControlPlaneManagedSyncLifecycle` against its own disposable Redis pass.
Regressions cover initial history, close/stale transitions, removed connections,
reconnection, idle mounts and Monitor counts. Independent review found no issues.
Safari at `http://127.0.0.1:5173/monitor` hot-updated from four reported mounts
to the one connected mount, removing the three historical nodes and their host
group while preserving workspace nodes and activity history. Both Vite and API
run from main; no backend source changes or server configuration changes were
needed. Logs: `/private/tmp/afs-topology-{ui-test,ui-lint,build,go-build,go-vet,go-test,go-race,process-test}.log`.

## Run the live development UI from main — 2026-09-22

- [x] Identify the API and Vite listeners and verify their source checkout.
- [x] Replace the stale worktree Vite process with `npm run dev` from main.
- [x] Verify the browser, API proxy and a real hot update without page refresh.
- [x] Document the live URL, rebuild/restart requirement and main-only workflow.

Review: API PID 78921 remains healthy on port 8091 from the main checkout.
Replaced the old worktree's Vite/npm processes with npm PID 95386 and Vite
PID 95407, serving `http://127.0.0.1:5173` from `/Users/rowantrollope/git/afs/ui`.
Command: `npm --prefix ui run dev -- --host 127.0.0.1 --port 5173 --strictPort`.
The Home page renders, `/v1/workspaces` returns 200 through the proxy, and
touching `ui/src/index.css` produces a browser-confirmed Vite hot update without
changing its contents. Browser console has no errors or warnings. Log and npm
PID: `/private/tmp/afs-ui-main-dev.log` and `/private/tmp/afs-ui-main-dev.pid`.
Only workflow documentation changed; no backend or Redis data changes. UI
changes now update live; backend changes still require `make control-plane`
and restart with the existing settings. No new branches or worktrees were used.

## Migrate upstream templates into Home recipes — 2026-09-21

- [x] Read current templates from the user-specified redis/agent-filesystem upstream.
- [x] Migrate all four recipes, starter files and agent guidance to mounted-folder AFS.
- [x] Link every Home recipe and the recipe index to complete, directly addressable guides.
- [x] Validate content, navigation and responsive rendering; rebuild and restart the active server.
- [x] Record results, commit and push main.

Scope: preserve upstream recipe substance and useful starter content while adapting
obsolete MCP-only setup to the current CLI. No provisioning backend or hosted MCP.
Existing Home styling and quickstart remain. Source checkout is read-only.
Review: migrated all five upstream manifests at revision `1ff1fa0589b0e01891f5137fe0ed6fb7cf3d5d2e`,
including 32 starter files, four skills and four command guides. Four Home cards
and the recipe index link to dedicated routes; Blank Workspace is available in
the collection. Complete setup prompts and file previews/downloads retain the
content while adapting MCP-only instructions to mounted folders.

UI lint, all 97 UI tests (14 new recipe regressions), `make control-plane`, Go
build/vet/unit/race checks and the isolated real-Redis managed-sync lifecycle
test pass. Browser checks verified all five recipe links, expanded file content,
the copy success state, and 390px collection/detail layouts without overflow.
Moved the long wiki attribution URL into a proper guide link after finding card
overflow. Browser console has no errors. Active server PID 74422 runs the main
binary; health/workspace API pass and all 14 entry assets match the new build.
Logs: `/private/tmp/afs-recipes-{ui-test,ui-lint,embedded-build,go-build,go-vet,go-test,go-race,process-test,server}.log`.
Authentication remains unchanged; user was asked separately about enabling API keys.

## Serve the latest main build — 2026-09-21

- [x] Inspect the active listener and preserve its launch settings privately.
- [x] Rebuild the embedded UI/server from main and replace the active process.
- [x] Verify process identity, health, API and served assets against the new build.
- [x] Record results and publish the task/lesson updates on main.

Scope: restart the current control plane with the latest local main build and
the existing Redis/authentication settings. Preserve database profiles and mounts.
Review: `make control-plane` passed. Replaced PID 11097 with PID 70508 from the
main checkout, preserving launch arguments and environment in memory. Health,
workspace API and the new API-key route pass. Served index bytes match the
fresh embedded build and differ from the previous server. Browser reload now
shows Home, cookbooks, Quickstart and API Keys, with build `5b3e63e594b9` replacing
the previous `980c8eda7218` build. Authentication remains unchanged. Build/server
logs: `/private/tmp/afs-latest-main-build.log` and
`/private/tmp/afs-latest-main-server.log`. Only task and lesson documentation changed.

## Restart the API-key control plane from main — 2026-09-21

- [x] Locate the inherited server on port 8091 and inspect launch settings without exposing credentials.
- [x] Rebuild the main checkout with `make control-plane`.
- [x] Restart from `/Users/rowantrollope/git/afs`, preserving its environment and connection settings.
- [x] Verify the listener, health, new API-key route and main executable/cwd; record the no-worktree correction.

Scope: replace the inherited d39a worktree process. Do not change Redis data,
server authentication settings, saved database profiles or mounted clients.

Review: gracefully replaced PID 60269 from the old worktree with PID 78921,
executing `/Users/rowantrollope/git/afs/bin/afs-control-plane` with cwd set to the
main checkout. Preserved all launch arguments and environment (updated PWD only),
without writing credentials to disk or output. Listener ownership, health and
the new API-key route verified on port 8091. The existing server has no team
token, so API-key issuance remains disabled; authentication was not changed.
Log: `/private/tmp/afs-control-plane-main.log`. Source workflow correction is
recorded in AGENTS.md and tasks/lessons.md. Existing worktrees were not deleted.

## Named administrator API keys — 2026-09-21

- [x] Add a shared Redis key registry, hashed secrets, expiry and atomic revocation/usage tracking.
- [x] Add authenticated key identity to API lifecycle and managed-session attribution.
- [x] Add `afs auth keys create/list/revoke` and preserve safe login/config handling.
- [x] Add API Keys UI with one-time secret display, expiration and revocation.
- [x] Verify build, vet, unit/race, isolated real-Redis process tests and browser flows.
- [x] Commit completed changes on main and push normally to origin/main.

Scope: user approved named keys for trusted administrators, with the existing
team token retained for bootstrap/recovery. All keys share server-wide access;
no workspace permissions, Cloud, MCP key families or additional catalog. API
revocation never claims to revoke issued Redis credentials or existing mounts.
Preserve the database management/checkpoint command work committed separately
as `0bcbdab`. Pin the key registry to startup Redis across database profile edits.

Review: Go build/vet, all unit and race suites, `make build`, embedded
`make control-plane`, UI lint and all 83 UI tests across 21 files pass. All 14
selected real-Redis auth/managed/database process tests pass (41.95 seconds),
including key creation/login, Redis and server restarts, expiry, scoped database
use, revocation and continued existing-mount I/O. Tests use disposable Redis only.
Independent review caught and fixed HTTP CLI checkpoint-author spoofing; key
identity now overrides the supplied author. Concurrency tests cover auth/revoke.
Browser QA against disposable Redis verified creation, one-time copy, secret
removal after closing, key identity/last use, self-revoke sign-out, rejected key
reuse, administrator recovery, light/dark layouts and clean browser diagnostics.
Build artifacts are refreshed; the existing user server was not restarted or
reconfigured. Evidence: `/private/tmp/afs-api-keys-{unit,race,process,build}-final.log`
and `/private/tmp/afs-api-keys-cli-build.log`; UI checks are in the task transcript.

## Consolidate database management and checkpoint naming on main — 2026-09-21

- [x] Integrate d39a database addition/editing, private persistence and scoped operations into main.
- [x] Integrate 29a9 full checkpoint command naming without the old cp alias.
- [x] Preserve the Home UI and main-only workflow; update Home recipes, skill and added-database acceptance to use checkpoint.
- [x] Verify all source additions from both worktrees are retained, with backups and original worktrees preserved.
- [x] Validate the combined main checkout before GitHub publication.

Review: make control-plane, Go build/vet and UI lint pass. Unit and race suites
each pass 1,040 cases with four existing optional Array skips. All 223 isolated
Redis integration cases, 75 UI tests and 38 lab-script tests pass. The original
CLI compatibility suite compiles; its comparison workload was not run. Existing
user Redis data, saved connection settings and running services were unchanged.
Logs: /private/tmp/afs-main-consolidated-*.log.

## Publish Home on main — 2026-09-21

- [x] Integrate the completed Home design, solid shared cards, larger typography and full-height navigation into main.
- [x] Preserve the existing sidebar work and both sets of review notes.
- [x] Record the user's main-only, no-new-branches, push-to-origin/main workflow in AGENTS.md.
- [x] Validate the merged main checkout with make control-plane, UI lint and all 41 UI tests.

Scope: completed work from the Home-page task. Separate database-management changes in the active development checkout remain intact.

## Fill the browser height with the UI sidebar — 2026-09-21

- [x] Trace the sidebar's percentage heights through the root layout.
- [x] Give the document, body and React root a definite full height.
- [x] Verify expanded/collapsed navigation, viewport resizing and content scrolling.
- [x] Run UI checks, embedded control-plane build and required Go checks.

Scope: restore the viewport-height layout and keep page scrolling inside the main
content pane. No Redis, CLI or service configuration changes.

Review: browser verification against a disposable Redis/control-plane instance
reproduced a 536px sidebar in an 1100px viewport with the old height rule and
confirmed the fixed sidebar reaches 1100px. All 24 combinations of two skins,
expanded/collapsed navigation, short/long pages and three viewport sizes pass.
Long content scrolls inside main while the sidebar remains in place; the profile
menu remains visible in a short viewport. UI build, 41 tests, lint, embedded
control-plane build, Go build/vet/unit/race and the isolated real-Redis managed
control-plane lifecycle test pass. Evidence: `/private/tmp/afs-sidebar-*`.

Live follow-up: the user's Safari page at `127.0.0.1:8091` was served from
`/Users/rowantrollope/git/afs`, which still had the old embedded CSS. Applied the
same source fix there, rebuilt with `make control-plane`, and gracefully restarted
only that server, preserving its original arguments and environment (PID 16784).
Reloaded Safari and visually confirmed the sidebar reaches the bottom of the
window with its footer at the bottom. Redis and mounted clients were not restarted.

## Match Home card surfaces — 2026-09-21

- [x] Reuse shared SurfaceCard for cookbook/Quickstart panels, recipe buttons, prompt and workspace callout.
- [x] Add standard card spacing and retain opaque fills in both theme families.
- [x] Build active embedded UI, pass lint and all 75 UI tests, restart and refresh live Home.

Review: live PID 58692 at `http://127.0.0.1:8091`. Browser confirms opaque
white cards in light mode and rgb(14,35,48) in dark mode, with the shared
4px corners and border treatment. Settings and database contents unchanged.
Logs: `/private/tmp/afs-home-cards-{build,lint,tests}.log`.

## Promote Home to the real instance — 2026-09-21

- [x] Apply the full-height sidebar correction to this preview checkout.
- [x] Merge Home into the active d39a checkout, retaining database features and its existing viewport fix.
- [x] Build/test the merged UI and restart the main server with its original launch settings.
- [x] Verify live Home, full-height navigation and reload the browser.

Scope: user explicitly requested the real instance, currently on port 8091
with Vite on 5173. Only source/UI merge, build and server lifecycle changes;
no database settings or contents are changed.

Review: active checkout build, lint and 75 UI tests pass. Main server restarted
as PID 57588 and healthy at `http://127.0.0.1:8091`. Browser shows the new Home;
root/sidebar both match 820px and 500px viewport heights. Main scroll changes
while body scroll remains zero and header stays at the top. Compact profile
control remains visible. Browser console has no errors/warnings. Launch arguments
and AFS environment were retained privately, then the temporary launch file was
removed. Logs: `/private/tmp/afs-home-live-{build,ui-tests,ui-lint}.log`.

## Learning-first Home — 2026-09-21

- [x] Adapt the supplied TypeSafe reference to the existing AFS design system.
- [x] Add Home and retain Monitor at `/monitor`, including View agents links.
- [x] Add agent/CLI quickstart, copy controls, downloadable skill and API access help.
- [x] Add four CLI-backed cookbook drawers and learning/navigation shortcuts.
- [x] Verify responsive light/dark design and primary interactions in the browser.
- [x] Complete UI/build/vet/unit/race and isolated Redis validation.

Scope: an implemented Home design in this checkout; one shared team token,
existing storage engine and Monitor preserved. Preview and tests use disposable
Redis only. No user installation, service, or saved configuration changes.

Review: `make control-plane`, UI lint and all 41 UI tests pass. Go build/vet,
unit and race pass (1,027 cases each, four optional Array skips); all 208
isolated Redis process cases pass. Browser checks cover light/dark, narrow
layout without horizontal overflow, exact clipboard content for agent/CLI,
cookbook and access drawers, the skill viewer, Docs and Monitor navigation.
No browser errors or warnings. Local preview: `http://127.0.0.1:8097`; its
Redis backend is disposable. Logs: `/private/tmp/afs-home-*.log`.

## New Home page in the active instance — 2026-09-21

- [x] Merge the reviewed Home UI from the 139b design checkout.
- [x] Preserve database settings/addition work and the full-height sidebar fix.
- [x] Build the embedded control plane, run all 75 UI tests and lint.
- [x] Restart the existing server with unchanged launch arguments and AFS settings.
- [x] Open live Home and verify full-height sidebar/independent content scrolling.

Review: server PID 57588 is healthy at `http://127.0.0.1:8091`. Home has the
learning banner, cookbook drawers, agent/CLI copy controls, skill viewer/download
and API-access/docs links. Monitor moved to `/monitor`; View agents follows it.
No database configuration or workspace contents changed. At 820px and 500px
viewport heights, sidebar/root match the viewport and the profile remains usable;
only main content scrolls. Browser console is clean. Logs:
`/private/tmp/afs-home-live-{build,ui-tests,ui-lint}.log`. Existing Vite source
also receives the changes. No commit or push requested.

## Review and edit database settings — 2026-09-21

- [x] Open prefilled settings from database rows and keyboard-accessible names.
- [x] Add validated atomic updates, password preservation/removal and revision checks.
- [x] Persist default connection edits and safely drain replaced Redis clients.
- [x] Restore definite viewport height so the sidebar fills the active app shell.
- [x] Validate browser flow, full checks and disposable Redis regressions.
- [x] Rebuild/restart the active control plane and verify the live development URL.

Scope: all database entries, including the startup/default connection, support
review/edit. Keep IDs and default selection stable. Preserve private credentials,
advanced Redis options and existing mounts; changing endpoints does not migrate
workspace data. Leave failed/stale edits unpublished. No removal/default-switch
controls requested. Existing uncommitted Add database changes belong to this
session and remain intact. No commit or push requested.

Review: database rows and keyboard-accessible names open a prefilled settings
dialog for default and added connections. Updates preserve omitted passwords,
support explicit replacement/removal, check connectivity before atomic private
persistence, reject stale revisions, and drain in-flight queries before retiring
replaced clients. Saved default overrides survive restart even if the original
startup endpoint is unavailable. Existing mounts keep their current connection
until remounted. Metadata edits preserve effective advanced Redis URL options.

Validation: build, vet and diff checks pass. Unit and race suites each pass
1,040 tests/subtests with four optional Array skips. All 219 isolated real-Redis
integration cases pass with no skips. UI passes 75 tests, full lint and final
`make control-plane`. Browser checks on disposable Redis verified default/added
settings, password preservation, failed-connection recovery, duplicate correction
and concurrent-edit rejection. Logs: `/private/tmp/afs-database-settings-*.jsonl`.

The user also reported the missing full-height sidebar fix. The root now has a
definite viewport height; the main panel retains independent scrolling. Read-only
checks of the live UI measured both sidebar and root at the full 720px viewport,
and confirmed the default database opens editable settings. Disposable browser
checks also passed expanded/collapsed navigation at 1152px height and independent
main-panel scrolling at 400px height; the profile menu remains usable. All QA
services were stopped and viewport overrides reset. Evidence is recorded in
`/private/tmp/afs-edit-database-browser-w6qed3s7/qa-results.json`. Backend restarted
from the final worktree binary as PID 36358 at `http://127.0.0.1:8091`; the existing
Vite UI remains at `http://127.0.0.1:5173/databases`. API and proxy return editable
database revisions without passwords or private connection URLs. No live database
settings or workspace contents were changed during verification.

## Restart the current server and enable UI live updates — 2026-09-21

- [x] Identify the current process and preserve its Redis/listener settings.
- [x] Restart port 8091 using the current worktree's rebuilt embedded server.
- [x] Start the existing Vite development UI on port 5173 and verify its API proxy.

Review: the user explicitly requested the restart and expected browser updates
while editing. The control plane is healthy at `http://127.0.0.1:8091` (PID 30440).
The hot-reloading UI is at `http://127.0.0.1:5173` (npm PID 30497); queued the
Databases preview in the app. Server and UI PID/log files live in the existing private
`~/.local/state/afs-lite/control-plane` directory. Both processes run this
worktree; original binary files and Redis content were unchanged. Checks were
read-only HTTP readiness, embedded/Vite asset presence and API proxy checks.
UI source edits hot-update on 5173; Go backend edits still need rebuild/restart.

## Restore Add database on the Databases tab — 2026-09-21

- [x] Trace the removed flow and retain the original dialog/API conventions.
- [x] Restore adding validated Redis connections with private atomic persistence.
- [x] Route workspace, session, history and CLI connections to the selected backend.
- [x] Validate UI, build, vet, unit/race and disposable real-Redis process tests.

Scope: the user's request restores adding self-managed Redis connections and
supersedes the previous one-backend restriction. Keep the startup Redis as the
default; reuse one storage engine per connection, without Cloud or a SQL catalog.
New connections are checked before saving in a private local file. No original
installation or existing user Redis data is used for tests. No commit or push
requested. Unresolved questions: none.

Review: restored the Add database dialog with URL paste, credentials, TLS and
index validation; save verifies Redis before publishing a private, atomic
connection profile. Added databases survive restart and use independent shared
engine instances. Workspace creation, browsing, history, checkpoints, sessions,
monitoring and CLI bootstrap preserve database scope. Concurrent aggregate
queries remain bounded when connections are unavailable; event cursors and UI
identities distinguish matching IDs on separate backends. Copied CLI commands
select the workspace's database and stop if login fails.

Validation: build, vet and diff checks pass. Full unit and race suites each pass
1,033 cases, with four optional Array skips; all 211 isolated real-Redis process
cases pass without skips. Final affected-package race checks pass 122 cases and
all three added-database process regressions pass after the last backend edits.
UI passes all 62 tests, full lint and `make control-plane`. Real-browser checks
verified adding a database, selecting it for workspace creation, scoped CLI
commands, and tree/content browsing on both disposable backends. The browser
check caught and fixed omitted scope in file-browser requests. All temporary
services were stopped. The rebuilt embedded server is in `bin/afs-control-plane`.
Evidence: `/private/tmp/afs-databases-{unit,race,integration}.jsonl` and
`/private/tmp/afs-databases-final-{race,integration}.jsonl`.
## Spell out the checkpoint command — 2026-09-21

- [x] Rename the public `afs cp` group to `afs checkpoint`, without an alias.
- [x] Update active help, docs, UI examples, scripts and CLI acceptance tests.
- [x] Verify build, vet, unit/race tests and isolated real-Redis process tests.

Decision: use full command names consistently and avoid overlap with Unix `cp`.
Checkpoint subcommands, options and storage behavior remain the same. This
supersedes the earlier decision to retain the `cp` group. Preserve original CLI
spellings in historical records and prior/current compatibility fixtures.
Unresolved questions: none.

Review: build/vet pass; rebuilt this checkout's `bin/afs`. Unit and race suites
each pass 1,027 test/subtest cases with four optional Array skips; all 212
isolated real-Redis process cases pass, including standalone and managed
checkpoint workflows and rejection of `cp` before config/Redis access. All 38
lab-script tests pass. The prior/current compatibility suite compiles; its
original-command side retains `cp`. UI changes are command text only; no UI
bundle was rebuilt. Existing Redis data and installed binaries were unchanged.
Evidence: `/private/tmp/afs-checkpoint-{unit,race,integration}.jsonl` and
`/private/tmp/afs-checkpoint-script-tests.log`.

## Publish complete control-plane, UI and auth work — 2026-09-18

- [x] Verify GitHub destination, branch ancestry and pending source inventory.
- [x] Confirm completed local build, unit/race, isolated Redis and UI checks.
- [x] Commit all pending source changes and push `main` to GitHub.
- [x] Verify the remote commit, clean checkout and GitHub check status.

Scope: user requested all current changes committed and pushed to GitHub.
Destination: `rowantrollope/afs`, default branch `main`; fetched remote and local
HEAD both start at `980c8ed`. Keep generated builds, dependencies, credentials
and runtime data outside Git. No unresolved questions.

Review: published all 179 changed source/docs/test/assets paths in `e883410` to
`origin/main` and verified the remote SHA matches. Working tree was clean.
Local validation is recorded below; GitHub CI run `35384146961` started and
was still running when publication was verified. The dependency symlink and
generated bundles remain ignored. This publication record is a documentation
follow-up; it does not change the tested product code.

## Restore the auth command workflow — 2026-09-18

- [x] Restore self-managed auth login, status, logout and help.
- [x] Preserve private, atomic config writes and verify access before saving.
- [x] Update CLI onboarding documentation and UI connection commands.
- [x] Verify build, vet, unit/race and isolated Redis login-to-mount workflow.

Spec: `afs auth login` connects to the saved/environment endpoint or defaults to
`http://127.0.0.1:8091`. Keep `--url`, `--control-plane-url` and `--self-hosted`;
accept the optional team token from stdin. Verify credential bootstrap before
atomically saving URL/token, without persisting distributed Redis credentials.
Preserve other config fields. Status reports effective settings offline; logout
clears saved managed settings offline and explains any remaining environment
override. Retain direct Redis mounted I/O and existing daemon behavior. No Cloud
login. Test with private configs and disposable Redis only.
Unresolved questions: none.

Review: rebuilt the installed derivative CLI and embedded UI/server. Build, vet
and diff checks pass. Full unit and race suites each pass 1,027 cases with four
optional Array skips; all 10 focused auth/config/managed CLI process cases pass
against disposable Redis. UI build, 41 tests and lint pass. Auth acceptance
covers token-protected login through actual mount/sync/unmount, unchanged config
on failed login, private permissions, old flag spellings, environment overrides
and offline/idempotent logout followed by standalone operation. Null optional
control-plane settings and saved-token isolation on URL changes have regressions.

The local UI/server is healthy at `http://127.0.0.1:8091` (PID 11097) and serves
the updated auth setup instructions. Verified default `afs auth login`, status
and logout against that server using a temporary configuration; saved user
settings were unchanged by this check. Evidence:
`/private/tmp/afs-auth-{unit,race,integration}-final.jsonl` and
`/private/tmp/afs-auth-{control-plane-build,ui-tests,ui-lint}.log`.

## Restore managed CLI and credential distribution — 2026-09-18

- [x] Define managed connection and typed API contracts around the existing engine.
- [x] Add authenticated Redis bootstrap and remote CLI management transport.
- [x] Route CLI management through HTTP and bootstrap sync/native mount credentials.
- [x] Verify URL-only setup, command parity, auth failures, credential handling and outages.
- [x] Run build, vet, unit/race and isolated Redis process checks; update docs.

Spec: a nonempty `controlPlane.url` selects managed CLI operation. Workspace,
checkpoint and history management use HTTP; mounts obtain the server's effective
Redis connection through authenticated bootstrap and retain direct Redis I/O.
No fallback to saved Redis on management failure. Explicit `--redis` selects
standalone operation for that command; an empty control-plane URL restores
standalone defaults. Preserve local mount flush/ownership guards and private
daemon bootstrap. Redis credentials never appear in ordinary API responses,
logs, status or saved user configuration. Keep one backend and shared team token;
no Cloud, catalog or ACL provisioning. Existing file access continues through a
control-plane outage. Test only disposable Redis and private config/state.
Unresolved questions: none.

Review: rebuilt CLI and embedded UI/server; build, vet and diff checks pass.
Final full unit and race runs each pass 1,004 cases with four optional Array
skips. All 206 isolated Redis process cases pass. UI build, 41 tests and lint
pass. URL-only management, password bootstrap, cross-mode local mount guards,
private native bootstrap and outage behavior are covered. Regressions reproduce
and fix a masked history authentication error and stalled HTTP transfers;
progress-based timeouts preserve long active transfers and release failed import
locks without publication. Updated setup documentation and UI connection copy.

The task-owned local server was restarted at `http://127.0.0.1:8091` (PID 7746).
Read-only availability checks confirm healthy Redis/UI and CLI discovery using
a temporary URL-only configuration. Saved user configuration was not changed.
The installed derivative CLI symlink uses the rebuilt `bin/afs`; the original
installation remains unchanged. No commit or push was requested or performed.
Evidence: `/private/tmp/afs-managed-{unit,race,integration}-final.jsonl`,
`/private/tmp/afs-managed-build-final.log`, and UI check logs with the same prefix.

## Compare old and new CLI connection workflows — 2026-09-18

- [x] Trace the original self-managed CLI setup and mount bootstrap.
- [x] Trace current AFS Redis configuration and optional management reporting.
- [x] Compare verified commands, credential flow and operational differences.

Scope: source inspection only; no product, configuration or service changes.
The requested `~/git/agent-workspace` path is absent; use the original
`~/git/agent-filesystem` checkout as the comparison baseline.
Unresolved questions: none for this baseline.

Review: inspected original checkout `1ff1fa0` and the current AFS working tree.
Original `controlPlane.url` selects self-managed mode; mount bootstrap resolves
the workspace and obtains Redis credentials through an HTTP session. Current
AFS requires independent Redis configuration; its control-plane URL enables
optional presence reporting, with a same-backend challenge and background retry.
Both use direct Redis file I/O. Commands and behavior were checked in source;
no old service or CLI was run and no runtime configuration was changed.

## Start local control plane and web UI — 2026-09-18

- [x] Inspect startup instructions, configured backend and available listeners.
- [x] Build current embedded UI and start the local control plane.
- [x] Verify the UI and read-only API, then provide the URL.

Scope: launch the current checkout using the CLI's configured Redis backend;
serve the embedded UI on loopback. No installation or Redis content changes.
Unresolved questions: none.

Review: `make control-plane` passed. Detached server PID 96982 serves
`http://127.0.0.1:8091`; the root page and all nine referenced assets return 200.
`/healthz` reports `{"ok":true}` and `/v1/workspaces` returns 200 with one
existing workspace. These were read-only availability checks. Server log:
`/private/tmp/afs-control-plane-run-9mftvacq/server.log`.

## Control plane and existing UI in AFS

Status: complete. Owner: Codex. Created/updated: 2026-09-17.

Goal: bring the useful original control plane and UI into this project, around
its existing storage engine and new CLI/daemons. Original source is reference
only; all new code lives here. Baselines: AFS `980c8ed`, original `1ff1fa0`.

Scope: one configured Redis backend; original Monitor/topology, workspaces,
file browser, checkpoints, History/versioning and Redis statistics; optional
managed daemon sessions with heartbeat/close and automatic mutation attribution;
a separate `afs-control-plane` executable with basic bearer authentication.
Keep mounted file I/O direct to Redis. Exclude Cloud, search, hosted MCP,
templates, multi-database administration and compatibility adapters. Preserve
existing UI design, prune unsupported entry points, reuse the shared engine.

- [x] Refresh checkouts and confirm the existing `afs:` namespace change.
- [x] Agree implementation boundary and divide backend, daemon and UI ownership.
- [x] Add the slim HTTP management API and shared engine adapters.
- [x] Register sync/native daemons, heartbeat/close, and attribute file activity.
- [x] Copy and adapt the existing UI without redesigning it.
- [x] Add separate server executable, embedded assets, build targets and docs.
- [x] Verify unit/race, UI build/tests, isolated Redis and browser acceptance.
- [x] Review changes and record evidence/remaining limitations.

Result: copied and adapted the existing UI, added the separate server, and
connected both daemon backends to optional management sessions. All new source
lives in this repository; the original checkout was used only as a reference
for this implementation. No installation, user configuration, existing Redis,
original service, commit or publication was changed.

Decisions: current user implementation approval supersedes earlier design-only
restrictions. `afs:` is the current storage namespace. Existing local state paths
stay unchanged. Session expiry describes liveness; it does not revoke direct
Redis credentials. Server checkpoints capture published data and do not flush
remote daemons. Tests use disposable Redis and temporary config/state.

Review: build/vet/diff checks pass. Final full unit and race runs each pass 975
cases with four optional Array skips. All 201 isolated Redis process cases pass,
including registration, idle heartbeat, file attribution, SSE, checkpoint
browsing, server outage/recovery, worker restart and closure. Native NFS RPC
attribution and startup-failure cleanup regressions pass. The UI passes a clean
public-registry npm install, typechecked production build, 41 tests and lint.

Real-browser acceptance verified token sign-in, managed-client topology/details,
file versions and exact activity links, checkpoint creation/comparison, workspace
creation/settings, Redis statistics, and live refresh after a direct daemon write.
A missing live root remains listed, can browse saved checkpoints, and was restored
through the final UI. Browser diagnostics are clean. Disposable services and tab
were shut down. Linux kernel native fleet and optional Array acceptance were not
rerun. Full evidence and capability boundaries are in
`docs/control-plane-validation.md` and `docs/control-plane-capabilities.md`.
Logs: `/private/tmp/afs-control-plane-{unit,race,integration}-final.jsonl`.

## Publish the Redis namespace correction

- [x] Locate the uncommitted prefix change and preserve it while updating main.
- [x] Apply `afs:` consistently to the newer file-history code and fixtures.
- [x] Validate the combined source with build, vet, unit/race and isolated Redis process tests.
- [x] Prepare the tested change for commit and publication to GitHub main.

The user reported the earlier correction was missing from GitHub. Integrate with
remote main `1b34090`, including the current history command group. Keep local
configuration/state paths and installed binaries unchanged; use disposable Redis.

Review: build/vet and full unit/race suites pass. Unit covered 922 passing cases
plus the added namespace/history regression in a focused run; race covered all
923 cases. Both suites skipped four optional Array-backend cases. The isolated
CLI and history process suites pass all 200 cases with no skips. The focused
regression checks literal `afs:` keys through publication, historical reads,
checkpoint fork/restore and activity, rejecting writes to another namespace.
Remaining `afs-lite:` literals are intentional retirement fixtures and migration
documentation. Logs: `/tmp/afs-prefix-publish-{unit,race}.jsonl` and
`/tmp/afs-prefix-integration.84kGIa`. Publication is authorized; the remote commit
will be verified after pushing this tested change.

## Original Redis namespace and control-plane compatibility

- [x] Compare current AFS with original control-plane, Redis records and web UI.
- [x] Change Redis keys from `afs-lite:` to `afs:`; retain local config/state paths.
- [x] Prove legacy discovery and namespace behavior with focused regressions and isolated Redis.
- [x] Validate build, vet, full unit/race and real-Redis process tests.
- [x] Document the work needed to reuse the original UI and safely adopt old data.

Scope: user requested the original Redis prefix and assessment of reusing the
original web UI after removing volumes/composition. No UI implementation,
automatic data migration, original installation changes or existing user Redis access.
Compare original `c3897ac` with current `8d7bcfa`; implement on current source.
Old content volumes map to current workspaces; old composed Agent Workspaces do
not. Shared namespace does not imply mixed-version write safety.

Review: build/vet pass; unit and race each cover 753 passing cases plus four
optional Array skips. All 120 isolated real-Redis process cases pass. A copied
original control-plane binary created legacy data: old-prefix CLI listed nothing,
updated CLI listed/read the tree and its checkpoints without Redis value changes.
Original control-plane also discovers updated CLI creations. Legacy mount still
rejects missing generation; writable adoption/mixed-version safety remain outside
this namespace change. See docs/old-control-plane-compatibility.md for UI reuse
scope and migration requirements. Evidence: /private/tmp/afs-old-compat-dwPeTW/.

## Keep HTTP serving in the control plane

- [x] Remove `history serve` and its listener/signal lifecycle from `cmd/afs`.
- [x] Retain control-plane HTTP contracts and test the original drawer with a test-only host.
- [x] Update current docs and the draft control-plane boundary; record the user correction.
- [x] Pass build, vet, unit/race and isolated real-Redis process tests; prepare the tested correction for PR #6.

User rejected HTTP serving in the CLI as bloat. History has seven client actions:
`list`, `show`, `diff`, `restore`, `undelete`, `export` and `policy`. HTTP handlers
remain in `internal/controlplane`; server hosting belongs to a separate control
plane, which is still a draft rather than a shipped runtime. Do not replace the
removed command with another CLI server or build the full proposed control plane
as part of this correction. Preserve compatibility through disposable test hosts.
This supersedes the prior optional `history serve` decision below.

Review: build, vet, full unit/race and isolated real-Redis process acceptance
pass. The original unchanged drawer passed all four component tests using the
direct control-plane handler fixture, including pagination, content/diff,
restore/undelete, capture-off activity and attribution. Recovered bytes and
ordinals match; fixture listeners closed and the original checkout stayed clean.
Removed-command regressions reject `history serve` before loading configuration
or connecting to Redis. Independent review found no lost handler capability or
remaining product server entry point. Storage and sync implementation are unchanged.

## Consolidate the file-history CLI

- [x] Review the overlapping root commands and confirm one `history` group with the user.
- [x] Consolidate listing, content, diff, restore, undelete, export, policy and the optional server under `afs history`.
- [x] Validate help, removed commands and the complete history process workflows; retain the original HTTP contracts.
- [x] Prepare the tested simplification for a focused follow-up PR after #5 merged.

User rejected the five added roots (`history`, `recover`, `versioning`, `file`,
`serve`) and selected one history group. Keep only `history` at the root and use
`list`, `show`, `diff`, `restore`, `undelete`, `export`, `policy`, and `serve`
beneath it. Use one grouped cursor-paginated listing, with all file lineages;
remove the competing compact listing and the old root commands without aliases.
The history engine and original web UI's HTTP contracts remain unchanged. This
user correction supersedes retaining the original CLI spelling.

Review: build, vet, full unit/race suites and isolated real-Redis process tests
pass. CLI regressions cover offline nested help, rejection of removed roots,
grouped pagination, export safety, policy, restore and undelete. The unchanged
original drawer passed all four component tests through `afs history serve`,
including actor attribution and activity while capture is off. Storage, sync and
HTTP implementation files are unchanged; earlier benchmark and kernel evidence
continues to identify its measured commit, not this CLI follow-up.
Publication uses `codex/history-command-group`; PR #5 already merged the engine
and its original CLI surface before this simplification was ready.

## Original file-history capability superset

- [x] Pin the original source and enumerate storage, policy, CLI and web UI contracts.
- [x] Restore content deduplication, equivalent-write suppression, hashes, attribution and metadata-only large-file records while retaining atomic capture.
- [x] Restore original history/content/diff/restore/undelete/checkpoint interfaces and a usable web UI compatibility transport.
- [x] Preserve history, shared-body ownership, lineage and retention through fork/restore/delete and concurrent writers.
- [x] Execute original-versus-new capability and performance comparisons on disposable Redis; record measurable improvements and remaining costs.
- [x] Pass build/vet/unit/race/process gates and review the complete capability matrix.
- [x] Prepare the tested commit and comparison evidence for the authorized GitHub PR.
- [x] Preserve ordinary sync session/agent/user attribution through background worker restarts and the unchanged history drawer.
- [x] Repeat final acceptance and paired measurements after attribution; reproduce and fix the stale-sync defect found during eight-client CI.
- [x] Prepare the final follow-up and evidence for PR #5, with remote kernel acceptance tracked in its checks.

User requires all original per-file versioning capabilities, including compatible
web UI interfaces unless an evidenced compromise warrants a UI change. Passing
the earlier reduced feature tests does not satisfy this goal. Retain the atomic
publication core and original retained sync/checkpoint code. Tests must use
owned infrastructure and leave the original installation and user data untouched.
Do not claim absolute absence of defects or production superiority from local
tests; measure both implementations with the same workload. Publication is
authorized through a PR, superseding the earlier main-only preference for this work.

Review: paired five-repetition runs on standard Redis and Array confirm
improvements in tested capture/recovery, retention memory and local write latency,
with higher memory in some unlimited-history cases. The exact final-code
measurements cover 240 scenario executions: the new version was faster in nine
of ten workload/backend pairs (1.75–3.20 times), while standard Redis retention
was 1.9% slower (1.159 to 1.181 ms). Five-version retention used 84.8–88.4% less
workspace memory. Costs
include up to 6.5% higher standard Redis memory and about 9% higher Array append
memory. These local measurements do not establish production superiority. Original drawer workflow
passed in a real browser; final component integration uses jsdom because supported
browser automation is unavailable while the desktop is locked. Compatibility
limits, attribution and storage differences are explicit in the report.

Build/vet, full unit/race, all isolated CLI/process acceptance, Array history and
recovery checks, native dependency races and 38 Python harness checks pass.
Regressions reproduced and fixed checkpoint recovery, empty-head diff fallback,
metadata-only pruning and safe test fixture port handling. No production
installation or user Redis has changed. The unchanged original component passed
all four final tests against the benchmarked source, including named actor attribution. Publication uses
`codex/file-history-superset` and the associated GitHub PR; remote CI supplies
the separate Linux and actual kernel mount gates. See docs/file-history-validation.md
for evidence, source hashes and the explicit production and compatibility limits.

## Initial optional per-file versioning implementation

- [x] Inspect upstream policy, lineage, recovery and retention behavior.
- [x] Capture immutable versions with accepted file, range, rename and delete publication.
- [x] Add root `history`, `versioning` and safe local `recover` commands.
- [x] Preserve independent history through forks, checkpoint restore and deletion.
- [x] Verify focused safety regressions, build/vet/unit/race and disposable-Redis process workflows.
- [x] Document default-off capture, retention, upgrade requirements and snapshot costs.

Design: workspace-wide Redis policy; history records published mutations and stays
separate from checkpoints. Recover into a new local destination without replacing
existing content, retaining exact bytes, modes and symlink targets. Stable lineage
survives rename; recreation gets a new lineage. Upgrade every writer before
enabling. Retention uses bounded batches and history-owned snapshots; logical
byte budgets are not total Redis memory limits. Original installation and user
Redis data remain untouched. See docs/file-history.md.

Review: build/vet and full unit/race suites pass; final unit/race runs use `-p 1`
after parallel acceptance suites exposed a disposable-server port collision.
Full CLI process acceptance, real-Redis storage tests, two independent history
writers with Redis restart, and standard/Array-backed history/fork/restore tests
pass. Regressions cover failed/lost-ack publication, first deletion, exact binary
and symlink recovery, stale writers, retention corruption/budget eviction,
checkpoint retries, rename lineage, source-name reuse and independent forks.
Local whole/range-write benchmarks measure retained memory and Redis script time;
see docs/file-history-validation.md. Actual kernel mounts and remote deployments
were not rerun. History remains off by default; enable only after upgrading all
writers. Source changes are local to this checkout; no installation or push.

## Integrate upstream parity and explicit sync verification

- [x] Integrate useful parity code/tests; preserve current main and existing edits.
- [x] Add `afs sync --wait <workspace|directory>` and `afs sync status [target]`.
- [x] Fix permission enforcement, unreadable-reader updates and stale chmod receipts.
- [x] Validate build/vet/unit/race, real-Redis CLI, concurrency and native checks.
- [x] Review final diff; record evidence and remaining platform limits.

Design: reuse save's existing authenticated control transport and verified receipt.
Wait requires one active writable folder-sync mount; reject native/read-only/ambiguous
or stopped targets. Status only observes sync activity. Wait resumes sync, creates no checkpoint, and fails clearly
on conflict/timeout/verification errors. JSON must remain machine-readable; human
output follows current REDIS header and timestamp conventions. Preserve main's
queue backpressure, symlink-mode and parallel save-verification fixes. Integrate
on main; test, commit and push after validation. Do not alter installed binaries/configuration during tests.
Tests use disposable Redis and private state. No unresolved questions.

Review: all three review regressions reproduced before fixes and pass after them.
Build/vet/unit/race, real-Redis CLI, original CLI compatibility, native dependency
and 38 Python checks pass. Linux nonroot build/vet/unit and actual FUSE ownership,
other-user permission denial and read-only smoke pass. Four-writer lab: 10/10;
paired Mac processes: 12/12 each, clean exits/cleanup. Mixed Linux native: 9/10;
known pre-existing NFS reconnect misses a newly created directory entry, recorded
without claiming a full pass. See docs/sync-parity-integration-results.md for
source/binary provenance, commands, artifacts and platform limits.

- [x] Prepare tested source for publication to GitHub main.

Publication authorized by the user; final remote verification follows the commit.


## Redis password environment override

- [x] Add AFS_REDIS_PASSWORD at client construction for CLI, sync and native mounts.
- [x] Document both environment variables and preserve URL passwords without new dependencies.
- [x] Verify build, vet, unit/race and isolated Redis authentication/process tests.

Design: --redis > nonempty AFS_REDIS_URL > JSON > default for the URL.
A present AFS_REDIS_PASSWORD, even empty, overrides the password in
the selected URL (JSON or --redis). Never serialize the environment password.
Unset preserves existing URL behavior. No unresolved questions.

Review: build, vet, full unit/race suites and focused real-Redis process test pass.
Tests cover URL/flag/password precedence, explicit empty password, literal special
characters, redaction, saved-password fallback, and authenticated background
sync with unmount/remount persistence. Native NFS export authentication tested
without an OS mount. Human config output suggests the password override; JSON
output is preserved. No dependencies added or user Redis data accessed.
Logs: /tmp/afs-env-{unit,race}.log.

## Redis context in CLI output

- [x] Add one credential-free database header to human database commands.
- [x] Show accurate mount endpoints in status and unmount, including mixed databases.
- [x] Verify build, vet, unit/race and isolated Redis CLI acceptance.

Design: compact Redis URL with explicit effective database; header before
operations/confirmation, after successful connection. Preserve JSON and offline
help/config output. Status stays offline; distinguish configured and mounted
databases. Preserve existing timestamp work. No unresolved questions.

Review: build, vet, full unit/race and isolated real-Redis process suites pass.
Effective DB overrides, credential redaction, offline/empty status, changed mount
configuration and JSON purity are covered. Rebuilt bin/afs (the installed symlink
target) and verified database override/list isolation through that exact binary.
Logs: /tmp/afs-header-{unit,race,e2e}.log. No user Redis data accessed.

## Delete all configured AFS workspaces

- [x] Inventory workspaces and local mounts through the installed CLI.
- [x] Unmount affected local mounts if needed; delete every listed workspace.
- [x] Verify the configured database's workspace list is empty.

Scope: user-requested deletion through `afs` using its current configuration.
Found 12 workspaces and no registered local mounts. No unresolved questions.
Review: the installed `afs` CLI deleted all 12 listed workspaces using
`afs delete <workspace> --yes`; every command succeeded. Final `afs --json list`
returned `[]`. No local unmounts were needed.

## Fix make install over an existing executable

- [x] Reproduce the refusal using an isolated install destination.
- [x] Allow regular-file upgrades while retaining directory/symlink safeguards.
- [x] Verify fresh/repeat/upgrade installs and configuration preservation; install locally.

Scope: Makefile installation and documentation only, on main. Preserve unrelated
branch-review notes and runtime data. No unresolved questions.

Review: the previous Makefile rejects a copy of the installed AFS executable.
The corrected rule recognizes its Go command identity and replaces it with a
staged checkout symlink, preserving unrelated files, directories and links.
Nine isolated checks pass, including repeat installs, paths with spaces,
configuration preservation and installing into the build directory itself.
`make install` now succeeds on the real installation; PATH resolves the correct
link and offline help passes. Configuration and the original `/usr/local/bin/afs`
link are unchanged. Build and diff checks pass. Evidence and prior binary:
`/private/tmp/afs-install-checks-1t73f4wc/`. Changes are local on main; not pushed.

## Review all branches

- [x] Inventory local/remote branches, worktrees and GitHub pull requests.
- [x] Compare every branch with live main; identify unique and superseded work.
- [x] Report cleanup candidates and preservation requirements.

Scope: branch review and record the user's main-only preference. Preserve
uncommitted work and the runtime Redis dump. No new branches.

Review against fetched main `8ad12fc`: seven extra branch names, six local and
six remote. `rtwork/slim-afs`, `rtwork/readable-cli-output`,
`rtwork/workspace-cli` and `rtwork/native-mounts` are fully merged (PRs 1–4).
Local-only `rtwork/control-plane-design` has no unique commits; its linked
worktree contains the superseded draft and task bookkeeping, not product code.
Main contains the improved final design.

Two branches contain substantive unmerged work: `rtwork/single-executable-redis-errors`
has two commits, including single-binary native mounting missing from main;
preserve main's newer Redis error wording when integrating it.
Remote-only `codex/upstream-parity-fixes` has one unique commit restoring bounded
import reuse, read-only mounts, native ownership options and directory-mode
safety, plus regressions/audits. Main also has its own newer sync/checkpoint
fixes, so preserve both sets rather than replacing main with either branch.
The parity branch's retained report records an unresolved NFS reconnect failure;
no tests were rerun for this ancestry/source review. No branches/worktrees were
deleted and no commits pushed. Main-only preference is recorded in lessons.

## Rerun on the user-selected replacement Redis database

- [x] Verify the same Mac/Sancho source and binaries from run 9cd72d09b414.
- [x] Prepare private per-run connection overrides without changing saved configuration.
- [x] Complete the unchanged 12-scenario workload on both actual systems.
- [x] Evaluate both reports, capacity counters and cleanup; retain review evidence.

User explicitly selected a different database for this rerun. Preflight reports
256 maximum clients, no rejected connections and no evictions. Keep the workload,
timeouts and tested code identical so the results can be compared with the prior
capacity-limited run. Use fresh workspaces; preserve prior artifacts and data.

Run a7145c3da397: all 12 scenarios pass on both actual systems; both runners exit
0. All 76 Mac and 80 Sancho saved tree comparisons are clear, including complete
bulk mutation, checkpoint, fresh hydration and normal unmount checks. No OOM
evidence, rejected connections, evictions or Redis error replies were recorded.
Cleanup reports are empty; owned processes and the new tunnel are gone. Saved
configuration and installed binaries are unchanged. Evaluation and provenance:
docs/two-system-results-2026-09-16-new-database.md.

Publication scope: the user requested committing the completed fixes, regressions,
runner diagnostics and result documentation directly to main. Private runtime
configuration and ignored test artifacts remain outside version control.

## Run fixed build on Sancho and macOS

- [x] Transfer and verify the same source snapshot; build separately on both hosts.
- [x] Run all 12 scenarios against the Redis configured on both machines.
- [x] Collect both reports and diagnose every failed/incomplete scenario.

User explicitly authorized this real two-host run against their existing Redis.
Use fresh named test workspaces and separate binaries/state; preserve existing
workspaces, installed binaries, server settings and original-run artifacts.

Run 9cd72d09b414: five complete passes (mutations, delete-edit, rename-edit,
partition, crash), six OOM-blocked cases, and a basic fresh-observer connection
failure. Redis exposes a 30-client limit and rejected connections rose from 0
to 9. Both runners exited 1 with no cleanup errors; owned processes and tunnel
are gone. The chmod and recovery symlink-unmount fixes pass across OSes; bulk
mutations remain unvalidated because OOM stopped initial creation. Full details:
docs/two-system-results-2026-09-16-fixed.md. Further acceptance needs memory and
connection headroom; no data cleanup or server settings change was performed.


## Fix failures from the first two-system run

- [x] Normalize folder-sync symlink modes without relaxing target conflicts.
- [x] Schedule guarded reconciliation for live directory chmods, including echoes.
- [x] Prevent saturated worker queues from blocking result consumption; recover deferred work.
- [x] Record Redis memory/error telemetry and distinguish capacity-blocked scenarios.
- [x] Parallelize bounded small-file save verification after reproducing the delayed checkpoint timeout.
- [x] Run focused regressions, build/vet/unit/race and disposable Redis process validation.

The subsequent actual Mac/Sancho run is recorded above. Local validation used
separate binaries and disposable Redis; the authorized real run used the existing
configured Redis with fresh test workspaces.

Validation: six new focused concurrency/mode/save regressions pass (the initial
three reproduced symlink-save, swallowed-chmod and full-queue failures before
fixes); build, vet, unit/race, native dependency checks, isolated CLI integration
and 38 Python regressions pass. All 12 paired scenarios pass on both local
processes with default workload and 45-second phases; the existing four-writer
lab passes all 10 scenarios, including its formerly intermittent rename/delete
case. A 12 MiB disposable Redis produces capacity-blocked large-file outcomes
and exit 1 on both peers. Owned cleanup succeeds, and pre-existing fixture
workspace metadata/cold-hydrated bytes remain unchanged. Credential-free local
validation reports are in tests/multiwriter/artifacts/two-system-fixes-2026-09-16.

The added 5 ms proxy-delay bulk run completed every mutation oracle, then hit
its fixed 120-second checkpoint save deadline. Bounded parallel verification
of files <=1 MiB fixes the serial read bottleneck without increasing that
limit. A focused 402-file delayed regression passes checkpoint (34.72s), cold
hydrate, normal cold/writer unmount (30.99/31.63s), exact retained trees and
cleanup. Full unit/race, CLI integration and all 12 normal paired scenarios
pass again on the final implementation. The full delayed mutation workload
itself has not been repeated after the save optimization.



## Evaluate actual macOS / Sancho acceptance results

- [x] Read completed reports, comparisons, daemon logs and saved baselines on both hosts.
- [x] Separate functional failures, unresolved backlog and Redis capacity failures.
- [x] Verify relevant test-owned Redis metadata and memory/eviction metrics read-only.
- [x] Preserve credential-free evidence and write docs/two-system-results-2026-09-16.md.

Run bdcfc93bc899: four passes, eight failures. Four failures share Linux fresh
observer symlink-unmount conflicts (0777 locally versus 0755 in Redis/macOS, with
zero saved modes interpreted as 0777). Directory chmod fails bidirectionally;
the existing-directory event handler returns without checking mode changes.
Bulk has 23 stale/missing/obsolete path discrepancies on the Mac at 120 seconds,
with 775 queued events and 77 tracked uploads on Sancho; root cause is unresolved.
Large-file edits and a shared-edit checkpoint hit repeated Redis OOM errors.
Recovery contents and fresh hydration pass before symlink unmount failures.
All owned-process cleanup reports are empty. Version labels differ, but the
tested commits have identical production Go code. No data was deleted, server
settings changed, workloads rerun, or production code modified during evaluation.

- [x] Fix and regress mixed-platform symlink baseline/mode handling at save/unmount.
- [x] Reproduce and fix bounded worker-queue deadlock; validate delayed bulk propagation.
- [x] Confirm the fixes in a new actual Mac/Sancho run with Redis headroom.
- [x] Add harness memory preflight/failure telemetry and capacity-blocked reporting.

The follow-up fixes directory chmod and a reproduced queue deadlock. The complete
replacement-database run a7145c3da397 above confirms all 12 scenarios across the
actual Mac and Sancho, including the previously incomplete bulk workload.

## Two-system synchronization acceptance

- [x] Add a coordinated macOS/Linux runner with isolated state and directories.
- [x] Check independent content/metadata oracles, bidirectional mutations, conflicts,
      partitions, warm crash recovery, ignores, flushes, and cold hydration.
- [x] Retain per-host diagnostics; reject missing peers, false convergence, and failures.
- [x] Document two-host SSH setup and validate the harness plus repository gates.

Scope: folder sync on two actual systems; orchestration travels outside the synced
tree. User clarified that both hosts must use their existing remote Redis server;
create fresh uniquely named test workspaces there, preserve existing workspaces,
and never restart/flush Redis or use installed AFS state. Only coordination needs
an SSH tunnel. Our local validation uses disposable Redis. Local paired-process
validation does not substitute for running on the user's two systems.

Validation: build, vet, unit/race and isolated CLI process suites pass. Harness
regressions cover independent oracles, authenticated coordination, per-case failure
recovery, remote proxy partitions and actual TLS hostname/trust verification.
Two complete paired-process runs finish 11/12 scenarios; both correctly return 1
for directory chmod after rename (expected 0750, peer remains 0755). A third,
earlier run independently reproduced that assertion before continuation existed.
Final workload: 100 files/writer/round, three rounds, 8 MiB large fixtures,
seed 1, 45-second phase deadline and 0.5-second stability window. All owned
process cleanup completes; a pre-existing fixture workspace retains its metadata
and cold-hydrated bytes. Production code/installation and remote Redis stay untouched.
The existing ten-scenario lab passed nine; its rename/delete candidate-preservation
check timed out once and passed a focused rerun. Retain that intermittent result.
See docs/two-system-sync.md for run instructions and local findings.

- [x] Investigate/fix live directory chmod propagation after directory rename.
- [ ] Investigate intermittent existing-lab rename/delete candidate loss.

## Align root help columns

- [x] Align every command and option description to the same column.
- [x] Rebuild and verify displayed help and repository checks.

All 15 command/option descriptions start in column 34 in the built help.
Build, vet, unit/race and isolated real-Redis process checks pass.

## Discoverable user configuration

- [x] Add config.example.json with every supported setting and its default.
- [x] Seed missing user configuration from make install without overwriting files.
- [x] Add offline config set with dotted keys, validation, private atomic writes,
      preservation of unrelated settings, and alternate --config paths.
- [x] Validate CLI behavior, installation preservation, build, vet, unit/race,
      and isolated real-Redis process checks.

User explicitly requested the config command family; workspace actions remain
at the root. Existing mounts retain their startup settings.

Validation: build, vet, unit/race and the isolated real-Redis process suite pass.
Regressions cover offline setup, saved connection use, alternate files and
one-command overrides, invalid-edit preservation, secret-free output, concurrent
updates, permissions, symlinks, and unknown-field preservation. make install
creates the full default file privately and preserves existing configurations.
The local binary was rebuilt and missing user configuration initialized.

## Per-user CLI installation

- [x] Link the current checkout's bin/afs into ~/.local/bin directly in make install.
- [x] Add make install, custom destination support, and setup/removal documentation.
- [x] Validate repeat installs, conflicting paths, PATH guidance, and CLI execution.
- [x] Run repository build, vet, unit/race and isolated process checks.

The Makefile recipe preserves existing unrelated commands and requires no sudo.
User clarified that installation must use make install without a separate script. The
existing local AFS link already points to this checkout.

Validation: make install and installed CLI help pass. Temporary-directory checks
cover default/custom destinations, spaces, repeat installs, PATH guidance, and
preservation of conflicting files, directories, and dangling symlinks. Build,
vet, unit/race and the isolated real-Redis process suite pass.

## Friendly Redis connection errors

- [x] Replace raw connection details with a friendly endpoint and --redis example.
- [x] Share the message with sync startup and retain credential redaction.
- [x] Verify exact CLI output in both modes and rebuild the installed binary.

Build, vet, unit/race and focused isolated CLI tests pass, including credential
redaction and the exact friendly error in normal and JSON modes.

## Quiet Redis connection failures

- [x] Reproduce extra Redis retry diagnostics through the installed CLI.
- [x] Disable internal Redis logging at CLI startup; preserve the final AFS error.
- [x] Pass build, vet, unit/race and isolated process checks; rebuild bin/afs.

Regression asserts one stderr line, empty stdout and exit 1 in text and JSON modes.
It fails on the prior binary and passes on the rebuilt installed binary. Build,
vet, unit tests and the complete isolated CLI process suite pass. Race packages
pass; afsfs required a rerun after its disposable Redis failed to become ready.

## Publish lightweight control-plane design

- [x] Verify source, scope and current GitHub main; isolate from other work.
- [x] Write proposed architecture, comparison, failure semantics and acceptance gates.
- [x] Check Markdown, references and documentation-only diff.
- [x] Commit directly to main, push normally and verify the GitHub document.

Spec: publish `docs/lightweight-control-plane.md` as a draft proposal.
Reuse the existing engine, optional `afs serve`, one Redis backend, persistent
access grants and direct Redis file I/O. Document permission/revocation limits;
do not implement features. Use an isolated main checkout and preserve the
shared checkout's branch. No unresolved publication questions; implementation
questions belong in the design.

Review: independent source audit passes against original `c3897ac` and slim
main `09f1146`. GFM parsing validates three tables, 11 relative links/anchors,
13 pinned source links and one Mermaid flowchart. All 11 original source paths
exist in GitHub's pinned tree. Only the draft and task bookkeeping change;
implementation and Redis tests are intentionally deferred. Design commit
`4e70584` was pushed directly to main; GitHub's main ref and exact document
bytes were verified afterward. The shared checkout stayed clean on its existing
`rtwork/single-executable-redis-errors` branch at `96dc5da`.

## PR #4 CI follow-up

- [x] Reproduce the missed startup deletion with a gated subscription regression.
- [x] Recover changes only after confirmed subscription; rescan when no cursor exists.
- [x] Reproduce and fix recycled-inode rename matching on an existing tracked path.
- [x] Isolate the manually queued inbound-read fixture from automatic recovery.
- [x] Preserve large-file chunk metadata when recovery wins the initial upload race.
- [x] Pass final local checks before updating the PR.
- [x] Isolate controlled checkpoint write fixtures from automatic startup recovery; retain conflict and timeout assertions.
- [x] Pass final fixture verification before publishing the PR update.

The original PR CI runs passed all four/eight-client native suites. One unit run
missed a remote deletion before subscription confirmation; the other run's core
smoke preserved an edit as an unexpected conflict copy. A deterministic reused-
inode regression reproduces the same canonical/conflict result and silent-copy
ordering. Historical inode reuse cannot be proven from those artifacts. The fix
keeps existing file/symlink baselines while retaining new-path rename detection.
Deletion and byte-preservation guards and workload deadlines remain unchanged.
Final local verification passes build/vet, 654 unit and 654 race test/subtest
cases with real Array enabled and no skips, 109 CLI cases, 14 dependency
regressions and 16 harness tests. All ten core scenarios also pass with Go 1.22
in isolated Linux processes. Source hashes stayed unchanged and owned cleanup
completed. GitHub PR #4 tracks publication and the merge gate; merge and pull
main only after the updated commit's CI checks pass.
The controlled checkpoint fixtures retain their safety assertions and normal
recovery on restart. Their complete group also passes ten repetitions per mode
(930 normal and 930 race test/subtest passes); final full verification still
passes all 654 unit/race and 109 CLI cases after the fixture-only changes.

## Check in native mounts and concurrency fixes

- [x] Verify the final source against retained acceptance evidence and review the diff.
- [x] Fix the close-time FUSE lock-owner regression found during check-in review.
- [x] Record the macOS FUSE approval blocker and run final repository checks.
- [x] Commit the tested implementation on a new branch for pull-request publication.

Scope: check in the tested implementation, harness, regressions and documentation.
The latest live macOS FUSE probe failed before readiness because kernelmanagerd
reported that macFUSE was not approved to load. Both test mounts timed out after
30 seconds; cleanup completed with no remaining owned processes. macOS FUSE
kernel acceptance remains pending user approval in System Settings. No unresolved
design questions remain.

Review: the final check-in source passes build/vet, 645 unit and 645 race
test/subtest cases (real Array enabled, zero skips), 109 CLI process cases,
14 vendored dependency regressions and 16 harness tests. The real Linux FUSE
lock scenario passes both second-descriptor POSIX close and same-session flock
wait/fsync/final-close checks. Cleanup reports no remaining owned processes.
The earlier integrated 68-scenario matrix is retained separately; the final
check-in also fixes the lock owner and sleeping-waiter admission issues found
in review. Source stayed unchanged during final verification. Publication uses
branch rtwork/native-mounts; GitHub CI reruns core and four/eight-client native
acceptance. See docs/multiwriter-results.md for evidence and platform limits.

## Native mount implementation and complete concurrency acceptance

- [x] Restore guarded inode I/O and lock interfaces in the shared Redis client.
- [x] Encapsulate retained FUSE/NFS adapters in an optional native helper.
- [x] Integrate root mount/unmount/status and checkpoint lifecycle across backends.
- [x] Resolve the known eight-writer conflict publication failure with a regression.
- [x] Extend concurrent acceptance to native and mixed mounts; run on available kernels.
- [x] Pass build, vet, unit/race, CLI compatibility and concurrent acceptance; document evidence.

Spec: keep folder sync as the default; add explicit `--backend=fuse|nfs`.
The native helper shares the current Redis client, Array detection, revision and
workspace generation guards. Keep protocol dependencies out of the CLI binary,
control state outside mountpoints, and native mounts visible to checkpoint and
restore/delete safeguards. Reuse original adapters without cloud/search glue.
Use disposable infrastructure only; preserve unrelated work and the original
installation. Completion requires passing tests, not only a successful build.
Review: all required local gates pass on final binaries. Build/vet, 635 unit and
635 race tests (including real Array, zero skips), 109 CLI cases, 12 prior/current
comparisons and 20 paused-writer repetitions pass. Core four/eight-client runs
pass all ten scenarios each on macOS and Linux; mixed FUSE/NFS/sync four/eight
client runs pass all ten each on Linux; macOS NFS passes all eight applicable
scenarios. Total concurrent acceptance: 68/68, with no setup/capture/cleanup
errors. Vendor race checks pass nine; harness checks pass 16; Linux native
package and FUSE cancellation checks pass 16 and five respectively.

Regressions cover directory modes, stale deletion/download results, local edits
during staging, checkpoint conflict moves, native startup/cancellation, parent
identity, generation/session fencing, cache recovery and crash cleanup. The final
eight-writer CREATE failure was false global WATCH contention; a deterministic
four-operation regression proves the narrow parent-guard fix. An occupied-port
RPC fixture now retries only EADDRINUSE; full Go checks run after native macOS
mount cleanup so their lsof scan has a stable mount table. Failed earlier reports
remain retained. Final source and binary hashes were verified after all runs.

Results and exact configurations: docs/multiwriter-results.md. Built binaries
are bin/afs and bin/afsmount. Native drivers stay out of the CLI dependency graph.
Tests used owned Redis and local Linux containers; original installations remain
untouched. macOS FUSE kernel testing needs an active driver; real Linux FUSE and
macOS/Linux NFS passed. Actual customer microVMs and remote CI were not exercised.
No product questions remain.

## Native mount encapsulation study

- [x] Inspect original driver layout and current retained client boundary.
- [x] Trace NFS/FUSE dependencies, write/flush behavior and lifecycle coupling.
- [x] Verify a minimal extraction boundary in a disposable copy where practical.
- [x] Recommend package/module layout, integration steps and acceptance gates.

Scope: architectural investigation; current production and original installation
stay unchanged. Unresolved questions: none required for the investigation.
Review: recommendation delivered; user subsequently authorized implementation.

## Multi-writer microVM workload lab

- [x] Inspect current CLI, sync guarantees, existing process tests and available runtimes.
- [x] Build configurable isolated clients, disposable Redis, fault injection and retained reports.
- [x] Exercise concurrent hydration/disjoint/shared writes, rename/delete, chunked files, partitions and crash recovery.
- [x] Reproduce findings; fix confirmed bugs with focused regressions where practical.
- [x] Run build, vet, unit/race and real-Redis process checks; document measured results and limits.

Spec: each simulated VM owns a local tree, HOME/config/state and an AFS daemon.
Only a disposable Redis server is shared. Use per-client TCP proxies for network
faults. Synchronize contenders before same-path mutations; check exact candidate
bytes, convergence and cold hydration rather than queue counts. Record seed,
versions, timings, status, logs and manifests; return failure on violated checks.
Provide a Linux container runner. Local process/container runs do not validate
microVM kernels, guest boot, KVM, production latency or customer scale.
Default: four clients; allow scale and seed overrides. Validate an eight-client
run as well. Customer target concurrency remains unspecified; the default is
a test setting, not a deployment capacity claim.

Historical review before native integration: lab reproduced remote-queue overflow dropping recovery requests,
abandoned uploads after EOF, unpublished startup conflict copies, stale renames
erasing peer edits, symlink conflict/recovery failures, unnecessary symlink
replacement on save, and completed inbound/outbound deletion markers suppressing
identical recreations. Focused regressions failed before the fixes. The final
four-client delete/recreate case passed. That eight-client folder-sync run had nine of
ten scenarios pass; one shared-creation candidate remains only as a local
conflict file on its origin while every queue is empty. This publication defect
was retained and documented; the later native integration work added regressions
and fixes for missed conflict-copy publication. See the current review above.
Bulk runs also exposed convergence and checkpoint costs; see
docs/multiwriter-results.md for exact parameters and retained evidence.
Docker Desktop host-bind flock failed an independent probe; the Linux runner
uses native tmpfs for workload execution and exports retained artifacts. That
environment ran, but final Linux validation is blocked by an unresponsive Docker
engine. Final build/vet pass; unit and race each pass 441 cases with three optional
Array skips; real-Redis CLI/process tests pass 109 cases; harness tests pass four.
Docker's graceful stop timed out. One lab-owned client remained in an OS exit
state after SIGKILL; its cleanup remains unconfirmed. No existing user Redis or
installed AFS binary was used or modified.

Customer-readiness follow-ups exposed by the lab:
- [x] Resolve the eight-writer live conflict-copy publication defect.
- [ ] Profile remote verification scans and repeated notification work.
- [x] Rerun final source on a healthy Linux runtime.
- [ ] Validate actual customer microVMs with their deployment environment.

## Remove file commands and flatten workspace actions

- [x] Delete public `fs` commands and their CLI-only helpers.
- [x] Move workspace actions to root; retain `cp`; reject removed `ws`/`fs` forms.
- [x] Update help/docs and adapt process/compatibility tests to mounted files.
- [x] Fix demonstrated parent-directory deletion retry/order issue.
- [x] Verify all retained behavior, readable defaults and explicit JSON.
- [x] Publish through normal CI/PR and update the installed derivative.

Decision: user selected root workspace actions with `cp` retained. File access
uses ordinary mounted directories. Internal storage/sync/Array support remains.
Unresolved questions: none.

Review: build/vet pass; unit and race each pass 403 test/subtest cases with three
optional Array skips. Process acceptance passes 109 cases; paired original/current
CLI comparison passes 12, with no skips in either. File operations now use real
mounted folders, preserving all retained sync scenarios. A parent-first directory
delete failure reproduced on original and derivative; the existing reconciliation
planner now retries it safely, including protection for a newer peer edit.
Focused race cases pass 30 repetitions. An initial disposable Redis startup
timeout did not recur in 30 focused repetitions or the full race rerun; captured
server logs showed no startup errors. Original checkout remains unchanged.
Published in PR #3. Linux build/vet/unit/race/process checks passed on `826b052`
in run `34919890029`. The installed slim AFS binary now uses that implementation
and passed all 109 process cases directly through its installed path.

## Default output correction

- [x] Reproduce JSON-default regression and inspect prior readable output.
- [x] Restore text confirmations, tables and details; preserve explicit JSON.
- [x] Verify both output modes through CLI contract and prior/current tests.
- [x] Run build, vet, unit/race and process checks.
- [x] Publish through normal CI/PR.

Scope: presentation only; `fs cat` keeps exact bytes. Unresolved questions: none.

Review: default-output regressions fail on the preserved pre-fix binary; original
CLI passes. Fresh derivative: 170 process passes and 12 prior/current comparison
passes, zero skips. Explicit JSON shapes and binary/empty `cat` are preserved.
Build/vet pass; unit/race each pass 401 cases with three optional Array skips.
Published in PR #2. Linux build/vet/unit/race/process checks passed on `ed59c1e`
in run `34917706289`; JSON schemas and sync/lifecycle behavior remain unchanged.

Baseline: redis/agent-filesystem `c3897ac05265444568a3819c21728a38a4ed254b`.
Target: rowantrollope/afs, private; existing initial commit `a91598c` preserved.

## Plan
- [x] Read brief, original guidance, inspect target and source provenance.
- [x] Inventory retained implementation and run baseline tests in a disposable copy.
- [x] Extract Go storage, checkpoints, filesystem client, and folder sync.
- [x] Remove cloud, composition/volumes, native mount drivers, search, MCP, and packaging.
- [x] Adapt CLI, isolated configuration/namespace/state, lifecycle and flush wiring.
- [x] Map prior CLI to reduced CLI and run black-box behavioral comparisons.
- [x] Execute every documented reduced command and README workflow through a built binary.
- [x] Verify final regressions; real Redis and independent-process acceptance; focused performance checks.
- [x] Update guarantees, limitations, provenance, additions, and final results.
- [x] Build, vet, test, race; commit, push and verify Linux CI.

## Decisions
- Original checkout and installed processes/configuration/data remain untouched.
- Baseline runs in `/private/tmp/afs-baseline-c3897ac`; only tracked source is copied.
- Retain private inode/tree storage and manifest checkpoint machinery. No sync rewrite.
- Independent agents inspect/extract storage and sync; root owns CLI/lifecycle and integration.
- Unresolved questions: none; investigate guarantees through tests before claims.

## Completion gate
The user explicitly evaluates completion through old-versus-new CLI behavior.
Reduced instructions must run successfully and preserve applicable prior behavior.
Package tests alone do not satisfy this gate.

## Review
All local acceptance gates pass on final production code:
- Build, vet and Linux cross-build.
- Unit and race: 396 test/subtest passes each; three optional Array skips each.
- Fresh-binary CLI/process suite: 135 passes, no failures or skips.
- Actual prior/current CLI comparison: nine passes, no failures or skips.
- Tests reproduce and fix ignored-file hydration, pending remote deletion during
  full reconciliation, and empty-file replay after a peer deletion.
- The pending-delete regression also fails on the original baseline; four
  focused sync cases pass 50 race-detector repetitions after the narrow fix.
- Production source is about 80% smaller. Original checkout remains unchanged.
- Linux CI passed build, vet, unit, race and real-Redis process acceptance on
  production commit `e983f4d`: run `34916337513`. Published through PR #1;
  visibility and protections remain unchanged.
## Human-readable display timestamps

- [x] Audit date/time display paths.
- [x] Use dd/mm/yyyy hh:mm:ss AM/PM (12-hour time) in the system local timezone for human output.
- [x] Verify build, vet, unit/race and isolated Redis output acceptance.

Scope: CLI tables, details and sync logs; preserve JSON/storage formats.
Unresolved questions: none.

Review: also covered version build dates and standard native-driver logs with
one shared formatter. Build, vet, unit and race suites passed. Four isolated
Redis output/CLI acceptance tests passed. Fixed build timestamp verified in UTC
and Asia/Kolkata (including next-day rollover); native helper error output verified.
Checkpoint IDs and conflict filenames retain their filename-safe timestamps.

## Capitalize Redis labels

- [x] Use REDIS in headers and detail labels; update existing assertions.
- [x] Rebuild and verify focused CLI output checks.

Review: focused unit and disposable-Redis output tests pass through rebuilt bin/afs.

## Home hero card consistency

- [x] Apply the shared opaque card background, border, corners and shadow to Learn Agent Filesystem.
- [x] Rebuild the active control plane and refresh the real UI at port 8091.

Review: make control-plane, UI lint and 75 UI tests passed. Live computed hero, cookbook and Quickstart surfaces match exactly in light and dark modes.

## Home typography readability

- [x] Enlarge every Home font-size rule, including responsive styles; widen Quickstart and increase prompt height.
- [x] Rebuild and restart the real UI; verify desktop (1230px) and narrow (390px) layouts have no horizontal overflow.

Review: embedded control-plane build, lint and all 75 UI tests passed.
