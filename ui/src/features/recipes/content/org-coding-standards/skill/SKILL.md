---
name: afs-coding-standards
description: "Use before writing, modifying, or reviewing code when the organization’s coding standards are available in an AFS-mounted folder. Read the relevant standards/ files, cite the path used, and treat the standards as read-only as a consumer agent."
---

# Org Coding Standards — read-only protocol

This skill reads your organization's coding standards from an AFS-mounted
folder. All paths below are relative to that folder. Use ordinary file tools
and treat the standards as a source of truth to consult, not a place for a
consumer agent to edit rules. A read-only mount receives updates without
publishing local edits; it does not limit the privileges of an API key.

## Before writing or modifying code

1. Identify the language and surface you are about to touch.
2. Read `standards/languages/<language>.md` when a matching file exists.
3. Read `standards/architecture-principles.md` and
   `standards/security.md` when the task crosses module boundaries, handles
   user input, touches secrets, or changes authorization behavior.
4. Apply the rules you read and cite the specific standard path when it affects
   the plan or implementation.

## When reviewing code

1. Read `standards/review-checklist.md`.
2. Check the diff against the applicable language, architecture, and security
   files.
3. Lead with findings, and cite the standard path behind each concern.

## If a standard seems wrong or missing

Do not edit this workspace as a consumer agent. Tell the user which file or
section is missing and ask whether a maintainer should update the standard.
