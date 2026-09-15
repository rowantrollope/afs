# AFS extraction

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
- [ ] Build, vet, test, race; commit, push, verify Linux CI and merge normally.

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
- Prior commit passed Linux CI. Latest fixes await commit, push and Linux CI
  before merging PR #1; visibility and protections remain unchanged.
