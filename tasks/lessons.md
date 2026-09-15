# Lessons

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
