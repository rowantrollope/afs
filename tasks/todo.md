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
- [x] Verify retained tests; real Redis and independent-process acceptance; focused performance checks.
- [x] Document guarantees, limitations, provenance, additions, and results.
- [ ] Build, vet, test, race; commit and push without changing visibility/protections.

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
- Build, vet, Linux cross-build.
- Unit and race: 387 test/subtest passes each; two optional Array skips each.
- Fresh-binary CLI/process suite: 134 passes, no failures or skips.
- Actual prior/current CLI comparison: six passes, no failures or skips.
- Root/flush/publication safety regressions pass; original checkout unchanged.
- Production source is about 80% smaller. Publication and Linux CI follow.
