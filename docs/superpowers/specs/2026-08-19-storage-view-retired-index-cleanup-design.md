# Storage View Retired Index Cleanup Design

Date: 2026-08-19

## Goal

Centralize physical View index garbage collection in one Storage View cleanup job. A successful A/B switch, a process restart, or a previously failed deletion must all converge through the same cleanup path. The cleaner must reclaim obsolete DuckDB and Bleve indexes without deleting an active, pending, or newly reused index.

## Scope

This change covers physical View index artifacts under the Storage View engine roots:

- DuckDB database files and their adjacent WAL files.
- Bleve index directories.
- Official A/B index slots and orphaned official View index artifacts.

It does not delete logical View metadata, Primary data, rebuild history, or temporary prepare artifacts. Existing engine startup handling remains responsible for `.prepare-*` files.

## Scheduling

The cleaner is registered with the tRPC timer component, not a Go `time.Ticker` or per-rebuild goroutine.

- Service name: `trpc.moox.storage.view.cleanup.timer`.
- Schedule: every 30 seconds.
- Handler: a `timerjob.Job` registered with `timer.RegisterHandlerService`.
- Handler timeout: 20 seconds, keeping one run bounded and preventing overlapping executions.
- The timer service is declared in `modules/storage/config/storage_view/trpc_go.yaml`.

The first tRPC timer invocation after startup performs discovery. It never deletes a newly discovered candidate in that invocation.

## Safety Model

An index is protected when any of the following is true:

1. Metadata identifies it as a View's current `active_index_id`.
2. Metadata identifies it as the index of an existing View build, regardless of build state.
3. The local runtime identifies it as `active` or `next`.
4. Its physical generation changed after the cleaner first observed it.

Metadata is authoritative. If the complete paginated View listing fails, the cleanup run performs no deletions and returns an error for tRPC timer observability.

Only artifacts returned by a View engine's managed-index listing API are eligible. The cleaner does not walk outside the configured DuckDB or Bleve roots and does not accept arbitrary filesystem paths.

## Candidate Lifecycle

The Service keeps an in-memory candidate map keyed by engine and index ID. Each candidate records:

- First unreferenced observation time.
- Index generation at first observation.
- Last successful observation time.

Each timer run follows this sequence:

1. Read every View from Metadata and build the protected index set.
2. Add local runtime `active` and `next` indexes to the protected set.
3. Ask each engine to list its managed physical index IDs.
4. Remove protected indexes from the candidate map and clear matching retiring state.
5. Record newly unreferenced indexes as candidates without deleting them.
6. For candidates unreferenced for at least 60 seconds, acquire the per-index gate.
7. Re-read Metadata and local runtime state while holding the cleanup guard.
8. Confirm the index remains unreferenced and its generation still matches.
9. Call the engine's `Remove` method and then clear in-memory mappings and retiring state.

A failed deletion remains a candidate and is retried by later tRPC timer runs. A failed metadata read never advances candidate age into a deletion decision.

The 30-second schedule and 60-second minimum unreferenced age mean normal deletion occurs roughly 60 to 90 seconds after first observation.

## A/B Activation Changes

Activation continues to switch Metadata and the local runtime atomically. It no longer starts a dedicated delayed deletion goroutine.

After switching from A to B, the old A index is marked retiring with its generation. This prevents `PrepareViewIndex` from reusing the slot before the cleanup timer has observed and removed it. The cleanup timer owns all physical deletion.

Retiring state is generation-aware. `PrepareViewIndex` checks and advances generation under the same per-index gate used by cleanup. If a candidate generation no longer matches, the old cleanup is cancelled without leaving a permanent retiring marker.

Initial View creation has no old active index and therefore adds no retiring candidate.

## Engine API

Add an optional managed-index listing interface to the View index model. DuckDB and Bleve implement it using their configured roots.

- DuckDB returns valid official `.duckdb` IDs and ignores `.wal` and `.prepare-*` names.
- Bleve returns valid managed index directories and ignores temporary or malformed entries.
- Physical removal continues through the existing `Engine.Remove` implementation, so DuckDB removes both the database and WAL while Bleve removes the directory.

## Logging And Observability

Each timer run emits structured logs for:

- Candidate discovered.
- Candidate protected again and cancelled.
- Candidate generation changed and cancelled.
- Index deleted successfully.
- Metadata discovery failure.
- Engine listing or deletion failure.

Logs include engine, index ID, first-seen time, age, and reason. They must not include authentication data or raw metadata requests.

The timer handler returns joined operational errors so the tRPC timer framework reports failed runs, while processing independent engines and candidates best-effort within the same invocation.

## Tests

Tests must prove:

1. A newly discovered orphan is retained during the first cleanup run.
2. The orphan is deleted only after 60 seconds of continuous unreferenced observation.
3. Active, pending-build, runtime-active, and runtime-next indexes are never deleted.
4. Metadata failure prevents every deletion in that run.
5. A process restart can rediscover and later remove an old A slot without persisted cleanup state.
6. A reused generation cancels stale cleanup and does not leave the slot permanently retiring.
7. Failed deletion is retried on a later timer invocation.
8. DuckDB removes `.duckdb` and `.wal`; Bleve removes its directory.
9. The tRPC timer service is present in the Storage View configuration and its handler registration fails fast when the service is missing.
10. Existing successful A/B switching remains query-safe while cleanup is pending.

## Non-Goals

- Persisting a cleanup queue in SQLite.
- Exposing manual filesystem deletion through RPC or CLI.
- Removing arbitrary unknown files outside managed engine roots.
- Changing the logical View lifecycle or rebuild trigger semantics.
