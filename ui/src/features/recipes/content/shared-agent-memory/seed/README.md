# Shared Agent Memory

A shared long-term memory for agents across your team. Every agent that
uses the mounted folder (Claude Code, Codex, Cursor, or another local-file
agent) reads from and
writes to the same memory, backed by Redis through Agent Filesystem.

## Layout

- `shared-memory/index.md` — curated rollup of all learnings, newest first.
- `shared-memory/entries/YYYY-MM-DD-<slug>.md` — one file per learning.
- `AGENTS.md` — the protocol every agent should follow when using this workspace.

## Why it's interesting

Redis backs the shared tree. AFS folder synchronization publishes local edits
and receives changes from other clients in the background. Work with ordinary
files in your mounted folder; use `afs sync --wait <mount-path>` on a writable
mount to verify that its local changes have been published. Other clients may
need time to receive those changes.

## Getting started

See `AGENTS.md` for the read/write protocol agents should follow. All paths in
that protocol are relative to the mounted workspace, not the computer’s root.
Keep each learning in its own uniquely named file; coordinate edits to the
shared index so concurrent agents do not overwrite each other’s changes.
