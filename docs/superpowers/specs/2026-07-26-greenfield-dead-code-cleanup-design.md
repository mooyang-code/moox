# MooX Greenfield Dead-Code Cleanup Design

**Date:** 2026-07-26

## Context

MooX is a new project. It does not need to read old data, accept old wire
formats, preserve removed API fields, or keep upgrade paths for deployed
versions. The repository still contains protocol reservations, deprecated
fields, old-schema migrations, compatibility fallbacks, adapters, and
unreferenced code left by earlier refactors.

This cleanup establishes the current design as the only supported design. It
also adds focused checks that prevent protocol reservations and known
compatibility paths from returning.

## Scope

The cleanup covers production code and its tests in:

- `modules/`
- `packages/`
- `web/`
- `web-host/`
- `scripts/`
- active protocol and schema definitions
- active README and architecture documentation when a removed contract makes
  them inaccurate

Historical specifications, plans, and verification records under
`docs/superpowers/` remain unchanged except for this design and its
implementation plan. Generated code changes only through the repository's
generators. Vendored dependencies, module caches, `node_modules/`, build
outputs, and unrelated worktrees are outside the cleanup.

The existing uncommitted `AGENTS.md` and schema SQL edits belong to the user.
The implementation must preserve and integrate them without reverting their
content.

## Cleanup Strategy

The implementation proceeds in dependency order so each batch leaves a
buildable contract:

1. simplify protocol sources and regenerate their consumers;
2. remove old-data and old-schema upgrade paths;
3. remove runtime compatibility fields, fallbacks, wrappers, and adapters;
4. remove statically unreachable Go and frontend symbols;
5. update current tests, scripts, and documentation;
6. run repository-wide verification and an independent code review.

A name alone does not prove that code is obsolete. Before deletion, inspect
direct references, interfaces, dependency injection, command registration,
RPC and HTTP routing, configuration-driven construction, reflection, build
tags, generated bindings, and external process entry points. Delete code only
when the current system has no valid path to it or when it implements a
superseded contract that the current design no longer supports.

## Protocol Rules

Delete every `reserved` declaration from Protobuf and Thrift sources. When a
reservation leaves a numeric gap, renumber the remaining fields or enum values
into a coherent sequence. Rebuild every generated client and server in the
repository in the same change.

Remove fields marked `deprecated` when they exist only to preserve an older
wire contract. Remove their server conversions, client fallbacks, tests, and
documentation at the same time. Do not replace a removed field with an alias,
dual-read path, or alternate JSON spelling.

The protocol sources become the single source of truth. A repository check
must reject future `reserved` declarations in supported protocol files.

## Data and Schema Rules

Keep only canonical fresh-install schemas. Delete code and commands whose sole
purpose is to:

- detect an old table or column;
- copy or translate old rows;
- drop retired tables after an upgrade;
- backfill a field introduced by an earlier version;
- accept multiple historical encodings of the same value;
- preserve an old index, trigger, or schema version.

Database startup may create the current schema and validate it. It must not
upgrade old databases. Existing local and remote SQLite data may be deleted
and recreated. Tests should create only the canonical schema unless they test
current database behavior.

## Runtime Code Rules

Remove compatibility request fields, old payload fallbacks, legacy status
normalization, no-op migration wrappers, retired CLI cleanup commands, and
adapters that only bridge superseded internal APIs. Move current behavior
directly onto the surviving interface when an adapter has no independent
domain role.

Remove commented-out implementations, abandoned feature branches, unused
types, functions, methods, constants, variables, exports, files, and
dependencies. Update dependent tests in the same batch. Keep validation and
guard scripts that prevent removed designs from returning; their references
to words such as `legacy` are assertions, not compatibility behavior.

Do not remove an active external-library shim merely because its comment says
`compatibility`. Such a shim remains valid when it adapts the current
third-party API rather than an older MooX contract.

## Verification

Verification must include:

- no `reserved` declarations in supported protocol sources;
- no production references to each explicitly removed field, command,
  migration, fallback, or adapter;
- clean protocol regeneration through `make proto`;
- focused tests for every touched module and package;
- frontend lint, tests, type checking, and production build when frontend
  code changes;
- repository boundary and contract checks;
- `make verify-pr`, followed by the broader proving set required by the
  touched surfaces;
- a final scan for commented-out code and stale compatibility markers;
- a clean worktree after committing all session changes;
- push verification against the exact remote branch SHA.

After implementation and local verification, start a new agent with no
implementation role. That agent reviews the complete diff for accidental
deletions, still-live compatibility paths, generated-code drift, broken
dynamic entry points, missing tests, and cross-module regressions. Fix every
confirmed finding and rerun the affected proving set before the final commit
and push.

## Completion Criteria

The cleanup is complete when:

1. supported protocol sources contain no `reserved` declarations or
   compatibility-only deprecated fields;
2. canonical schemas have no old-data migration or cleanup path;
3. current call chains contain no confirmed compatibility-only or dead code
   found by the audit;
4. generated sources match protocol inputs;
5. focused and repository-level verification passes;
6. the independent review has no unresolved finding;
7. every file changed during the session is committed and the exact commit is
   present on the remote feature branch.
