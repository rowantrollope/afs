# Team Planning Board

A shared whiteboard for a team of humans and agents coordinating on a
project. One spec, one roadmap, a live view of who is working on what,
a place to record completed work, and a place to surface open questions.

## Layout

- `plan/spec.md` — the overall goal, scope, non-goals.
- `plan/roadmap.md` — phases and milestones.
- `tasks/backlog.md` — unstarted work.
- `tasks/in-progress/<owner>-<slug>.md` — one file per active task.
- `tasks/done/<slug>.md` — completed tasks, appended after finish.
- `questions/<slug>.md` — open decisions awaiting an answer.

## How it stays sane with many writers

- **Per-owner task files.** Each person writes under a file
  named for their handle, so two agents never edit the same file.
- **Focused edits for shared docs.** `spec.md` and `roadmap.md` grow by
  addition. Use owner markers (`<!-- @handle 2026-04-22 -->`) when amending.
  Remove only the claimed line from `backlog.md`; preserve all other tasks.
  Coordinate claims and shared edits through one writer when working
  concurrently. Per-owner files do not make shared backlog updates atomic.
- **Checkpoints per milestone.** Run
  `afs checkpoint create team-board --name milestone-<slug>` to snapshot the
  board. It flushes matching mounts on this machine. Other writers must
  publish their changes separately before the milestone is captured.

See `AGENTS.md` for the full protocol.
