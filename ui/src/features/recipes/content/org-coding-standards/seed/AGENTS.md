# Protocol for this workspace

This workspace contains your organization's coding standards. Treat it as
**read-only** when acting as a consumer agent. Consult the files in the mounted
folder before writing code and cite them in reviews. Use a read-only AFS mount
when you need local edits to stay out of the shared standards.

## Before writing or modifying code

1. Identify the language and surface you're about to touch.
2. Read the matching file under `standards/languages/`.
3. Read `standards/architecture-principles.md` and
   `standards/security.md` when relevant.
4. Apply the rules you read. If any rule conflicts with the user's
   request, surface the conflict and ask how to proceed — do not
   silently override the standard.

## When reviewing a PR or diff

1. Read `standards/review-checklist.md`.
2. Walk each item against the diff. Cite the specific standard file
   and section when you flag something.

## If a standard seems wrong or missing

Do not edit it as a consumer agent. Instead, raise it with
the user and point at the specific file and line.
