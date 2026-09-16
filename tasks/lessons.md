# Lessons

## 2026-09-15 — Ship one executable

User prefers a single `afs` executable, including optional native mounts.

- Keep implementation boundaries inside packages and background processes;
  do not require another installed executable for an AFS backend.
- Measure combined size before assuming separate executables reduce downloads.
- Validate the distributed CLI alone, including native startup and recovery.

## 2026-09-15 — Re-exec test children must bypass the test runner

- Handle a private daemon invocation directly in `TestMain` and exit. A daemon
  child that falls through to `m.Run` can recursively launch the whole suite;
  do not depend on rewriting command-line arguments to select its fixture.
- Give gated process fixtures a lifetime limit, even when parent cleanup exists.
- Coordinate shared-checkout verification before changing process-launch tests;
  freeze source and prove focused child dispatch before running the full suite.

## 2026-09-15 — Connection failures need CLI presentation

User clarified that Redis connection attempts should be silent; print only an
endpoint-specific error when Redis is unavailable.

- Present bounded connection attempts through the CLI; avoid exposing repeated
  driver retry logs or raw nested network errors as the user-facing result.
- Do not print a connecting banner in any output mode. Successful commands keep
  stderr empty; failures get one clean stderr error with an endpoint and no secrets.
- Verify unavailable Redis through the built CLI alongside successful and offline
  commands, rather than testing only error-formatting helpers.
- When concurrent tasks share this checkout, coordinate file ownership and run
  final verification from a frozen snapshot. Do not test or install another
  task's partially edited production code.

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
