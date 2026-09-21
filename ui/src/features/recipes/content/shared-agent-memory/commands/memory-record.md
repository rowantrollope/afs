---
description: Force-record a durable learning into shared memory, bypassing the auto-skill's judgment on durability.
---

Record a new entry in shared memory. Use the title the user provides.

1. Ask the user for Context, Finding, and Sources if they didn't provide
   them in the command arguments.
2. Pick a slug from the title: kebab-case, prefixed with today's date
   (YYYY-MM-DD). If unsure about collisions, append a 4-char random suffix.
3. Create `shared-memory/entries/<slug>.md` using the standard entry template
   (frontmatter: date, agent; sections: Context, Finding, Sources, Keywords).
4. Read `shared-memory/index.md`, then add a one-line pointer under today’s
   date heading with a focused local edit. Coordinate shared index updates
   through one writer when agents work concurrently. Never overwrite an entry
   or assume that local appends on separate machines are atomic.

Title: $ARGUMENTS
