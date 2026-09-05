# Coin Selection Runtime

> **范围说明：** 下文保留 V2 Manifest、Runner 和独立 outbox 的实现说明，不是新目标合同。
> 后续 StrategyDef / StrategyInstance、instance_id / session_id、strategy_name 与三表方案以
> [策略执行框架设计](../../../docs/策略执行框架设计.md)和
> [实施计划](../../../docs/superpowers/plans/2026-08-29-moox-coin-selection-strategy.md)为准；
> 本轮仅更新文档，不表示这些能力已实现，不能将下文旧字段用于新增方案。

The Strategy service is a declarative, Go-only runtime. A `moox.strategy/v2`
Manifest is validated and compiled into an immutable dependency record before
it is persisted. The record freezes the source View, Factor Binding, result
View, and concrete factor output columns; enabling a Runner re-verifies those
identifiers without selecting replacements. It is not a persisted period-input
snapshot and does not make the mutable Storage index replayable.

`ViewFactorPeriodReady` is the runtime trigger. The event identifies a
completed Factor result View period, carries per-binding terminal states, and
includes the source/result active-index IDs used for that computation.
Strategy evaluates a Runner only when every binding referenced by that Runner
is complete; an unrelated degraded binding in the same View does not block it.
The compiler requires all factor outputs to share one Result View. The Storage
RPC reader first pins the active index and subject selectors for the source and
Result Views. Every history/current query then
carries the pinned index as `expected_active_index_id`; a cutover during the
read returns `VIEW_NOT_READY`; the ready delivery is recorded as superseded and
ACKed rather than retried forever. Strategy never composes rows from different
View generations. Storage also returns an
`active_index_revision` and rejects a page when the same physical index was
updated in place during the read; subsequent pages carry
`expected_active_index_revision`. The Runner loads the complete configured
instrument pool and all frozen factor columns for that period. Binding-level
degraded states are scoped against the loaded pool using both `subject_id` and
`instrument_id`; a failed subject that is outside the configured pool does not
veto this Runner, while a selected subject always does. Without a loaded pool
the check remains conservative.
`strict` readiness distinguishes two outcomes: an unfinished View or temporary
Storage failure returns RETRY; a stale View provenance generation is terminally
superseded and ACKed; a readable View with a missing pool
row or required column records `last_error`. If the Factor-ready event is
already terminally degraded, the inbox is ACKed after recording the failure so
an unrecoverable period cannot poison the unlimited-delivery consumer; a
complete marker remains retryable until a matching ready marker is reissued.
Permanent dependency responses such as a deleted Factor or View are classified
as dependency mismatches and ACKed after recording the Runner failure. The
verification is performed only after the ready event matches that Runner, so an
unrelated View event is not blocked by another Runner's dependency outage.

The evaluator ranks and filters the full pool, allocates deterministic fixed
precision weights, merges long and short sides by instrument, and emits a FULL
`target_weight` snapshot. Strategy persists every successful period result;
unchanged targets are recorded as `hold`, while changed targets advance the
command sequence and enqueue one typed Trade event in the same SQLite
transaction. Strategy reads the Trade owner generation again after commit and
requires the account to still be owned by this Runner; if either owner or
generation changed during a long calculation, the local live snapshot and
pending outbox row are invalidated and the ready event remains retryable
instead of silently becoming a hold. A retry whose previous result belongs to
an older generation is forced to emit a fresh rebalance, even when weights are
unchanged.

Disabling and enabling a Runner starts a fresh active lifecycle: Trade clears
the live convergence target on ownership claim, and Strategy clears its cached
targets so the next successful period is emitted as a new rebalance rather than
an unobservable hold. Immutable Strategy/Trade history and `TargetReceipt`
rows are retained.
Trade also persists a monotonic `owner_generation` lifecycle token. Strategy
puts the token in each target event and Trade accepts only the current
generation, covering messages already published but still in flight during a
release/reclaim transition without comparing wall clocks across services.
Strategy retries release for disabled Runners at startup and periodically,
treating an owner-conflict response as an already-completed release. Archived
same-ID takeover uses a durable `rebind_key`; Trade applies each key once and
returns the resulting generation, so a response/marker retry cannot delete a
newer target. Strategy performs owner reconciliation before starting the
EventBus relay/ready consumer, and deployment waits for Trade `/readyz` before
starting Strategy.

Database startup performs safe migrations before strict schema validation. Trade
adds the known owner-generation column to an older logical-account table and
initializes existing owners at generation one (unowned rows remain zero). Strategy V1 tables are renamed into
`legacy_strategy_v1_*` archive tables because their source-code and trigger
fields have no V2 execution meaning; unknown or partially malformed schemas
remain fail-closed instead of being reset implicitly. A startup/periodic
reconciler releases archived Trade owners under the runner lock. When an
ENABLED V2 runner has deliberately taken over the same space/account binding,
it calls Trade's explicit rebind lifecycle operation, which advances
`owner_generation` and clears the old live target without dropping ownership or
pausing automation. Successful release or rebind is marked in the archive table
so later passes do not issue duplicate Trade calls.

Trade owns the only weight-to-quantity conversion. It uses authoritative
enabled-and-ready member equity, separate Storage Primary/Metadata and DataView
credentials, and freezes the selected member, venue reference prices, and
resulting raw quantities in an immutable `t_logical_account_target_receipts`
row before normal execution quantization and order placement. The executor
must not silently switch member after this receipt is written. Replaying the same target ID is idempotent;
reusing it with a different request hash is a terminal conflict.
