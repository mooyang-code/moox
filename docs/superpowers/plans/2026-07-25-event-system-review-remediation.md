# Event System Review Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve all six findings from the 2026-07-25 event-system review with small, source-backed changes and real E2E proof.

**Architecture:** Keep the existing Event Registry, JetStream transport, and module-owned consumers. Add monotonic Trade command acceptance, a reconnecting Factor session loop, Registry-owned semantic validators, one canonical local EventBus endpoint, and remove configuration that no runtime behavior reads.

**Tech Stack:** Go 1.25, Protocol Buffers, NATS JetStream, SQLite/GORM, Pebble, Bash.

---

### Task 1: Reject stale Trade commands

**Files:**
- Modify: `packages/tradeeventpb/trade_events.proto`
- Modify: `modules/strategy/internal/store/commit.go`
- Modify: `modules/strategy/schema/strategy.sql`
- Modify: `modules/trade/schema/bus.sql`
- Modify: `modules/trade/internal/infra/store/store.go`
- Modify: `modules/trade/internal/application/rebalance/service.go`
- Modify: `modules/trade/internal/bootstrap/kernel_workers.go`
- Test: `modules/trade/internal/bootstrap/kernel_workers_test.go`
- Test: `modules/trade/internal/application/rebalance/service_test.go`

- [x] Add failing tests proving a lower sequence is recorded but creates no run.
- [x] Run the focused tests and confirm failure is due to missing sequence behavior.
- [x] Generate the protobuf after adding `uint64 command_sequence = 11`.
- [x] Atomically advance the per-execution-binding producer sequence before creating a rebalance.
- [x] Run Trade and Strategy focused tests.

### Task 2: Recover the Factor live consumer

**Files:**
- Modify: `modules/factor/internal/trigger/nats.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Test: `modules/factor/internal/trigger/nats_test.go`
- Test: `modules/factor/internal/bootstrap/bootstrap_test.go`

- [x] Add failing fake-session tests for ready, failure, reopen, and recovery.
- [x] Add a small session interface and fixed-delay reopen loop.
- [x] Include the enabled consumer's readiness in Factor health.
- [x] Run focused tests and race tests.

### Task 3: Centralize event semantic validation

**Files:**
- Create: `packages/events/validation.go`
- Modify: `packages/events/registry.go`
- Modify: `packages/events/message.go`
- Modify: `packages/events/decode.go`
- Test: `packages/events/events_test.go`
- Test: module consumer tests affected by validation

- [x] Add table tests that mutate every payload/envelope identity and confirm rejection.
- [x] Confirm Encode and DecodeRaw tests fail before implementation.
- [x] Register one validator for each of the five events.
- [x] Invoke validators on every encode, decode, and outbox publish path.
- [x] Remove duplicate consumer-local identity checks where they add no extra policy.

### Task 4: Remove configuration drift and dead compatibility

**Files:**
- Modify: `modules/cloudnode/config/app.yaml`
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_client.go`
- Modify: `scripts/deploy/deploy-moox.sh`
- Modify: Storage, Monitor, Archive, and Factor event config types and YAML
- Test: affected config and deployment tests

- [x] Add a config test loading the checked-in CloudNode YAML and expecting port 4222.
- [x] Remove the deployment-time 4322 replacement.
- [x] Remove Stream/topic/topology/embedded fields not read by runtime behavior.
- [x] Update tests to assert the reduced configuration surface.

### Task 5: Add real critical-path E2E coverage

**Files:**
- Modify: `scripts/check/verify-event-contracts.sh`
- Add or modify: Strategy/Trade self-contained E2E test
- Add or modify: Storage consumer fan-out E2E test

- [x] Start embedded NATS inside each E2E rather than requiring an environment variable.
- [x] Exercise actual Strategy outbox and Trade handler in one test.
- [x] Exercise actual Storage event handlers instead of placeholder durable names.
- [x] Add Factor, Archive, CloudNode, and E2E packages to the verifier.

### Task 6: Independent review and completion proof

- [x] Run focused unit and race tests.
- [x] Dispatch a fresh review Agent with the six findings and full diff.
- [x] Fix every Critical or Important review issue and rerun affected tests.
- [x] Run `./scripts/check/verify-event-contracts.sh`, deploy tests, E2E tests, race tests,
      `git diff --check`, and inspect the final worktree.
