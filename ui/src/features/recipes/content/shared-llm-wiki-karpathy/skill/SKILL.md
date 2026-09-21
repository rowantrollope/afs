---
name: afs-shared-llm-wiki
description: "Use when answering non-trivial questions in a project with an AFS-mounted shared LLM wiki. Read wiki/index.md, search wiki/ for key nouns, cite relevant paths, and offer to file durable answers. When ingesting raw/ sources, update source, topic, and entity pages, the index, and the log."
---

# Shared LLM Wiki (Karpathy style) — agent protocol

This skill operates a shared LLM-maintained knowledge wiki in an AFS-mounted
folder. Use ordinary filesystem tools; all paths below are relative to that
folder. The workspace has immutable raw sources under `raw/`, maintained
wiki pages under `wiki/`, and a protocol file at `AGENTS.md`. AFS synchronizes
changes in the background; use `afs sync --wait <mount-path>` on a writable
mount when you need to verify that your local changes have been published.

## Before answering a non-trivial question

1. Read `wiki/index.md`.
2. Search `wiki/` for the key nouns using
   `rg`.
3. Read the most relevant pages.
4. Answer with citations to wiki paths.
5. If the answer is durable, ask whether to file it under
   `wiki/syntheses/` or `wiki/questions/`.

## Ingesting a source

1. Confirm the source path under `raw/`.
2. Read the source.
3. Create or update a source note under `wiki/sources/`.
4. Update relevant pages under `wiki/topics/` and `wiki/entities/`.
5. Update `wiki/index.md`.
6. Append a parseable entry to `wiki/log.md`:
   `## [YYYY-MM-DD] ingest | <Source title>`.

## Maintenance

When asked to lint the wiki, check for missing sources, stale claims,
contradictions, orphan pages, missing concept pages, and unanswered questions.
Report suggested fixes first unless the user explicitly asks you to clean them
up.
