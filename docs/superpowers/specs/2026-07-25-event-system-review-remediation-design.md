# Event System Review Remediation Design

## Goal

Fix the six findings from the current event-system review without expanding the
system beyond a single-node personal quant deployment.

## Boundaries

- Keep the seven-field `EventMessage` envelope and the five registered events.
- Keep NATS JetStream, the four Streams, and the CloudNode KV bucket.
- Keep Storage and Strategy transactional outboxes and the Trade inbox.
- Keep Storage View's subject-keyed dispatcher.
- Do not add a generic keyed runner, shared DLQ, schema service, cluster, or
  exactly-once protocol.

## Design

### Trade command ordering

`TradeRebalanceRequested` gains a required `command_sequence`. Strategy
allocates it from a transactional counter keyed by `execution_binding_id`, so
commands stay monotonic even when several strategy bindings feed one execution
group. Trade atomically records the message inbox row, compares the sequence
with the last accepted sequence for the
`(consumer, space_id, execution_binding_id)` lane, and creates a rebalance only
when the sequence advances. Duplicate and stale messages are acknowledged
without creating a run. This protects ordering across retry, reconnect, and
process restart without introducing a scheduler.

### Factor consumer liveness

Factor owns a reconnecting consumer session loop. Startup still fails when the
initial connection cannot be established. After startup, a terminal Runner
error marks the consumer not ready, closes the failed session, waits one second,
and opens a new session until successful or the service context is cancelled.
Factor readiness includes the live consumer state whenever realtime NATS is
enabled.

### Event semantic validation

Each Registry event owns one semantic validator. Encode, DecodeRaw, and
PublishMessage all execute the same validator. Validators enforce the identity
relationships already implied by publishers:

- Cloud job item ID equals event ID and route equals code package/job type.
- Host agent ID equals subject ID.
- Metrics service/instance equals subject ID.
- Storage space/dataset and every row identity equal the envelope.
- Trade request ID equals event ID, execution binding equals subject ID, and
  command sequence is positive.

Typed Storage decoding remains as a convenience cast but no longer owns unique
semantic validation.

### Configuration cleanup

CloudNode's checked-in URL is `nats://127.0.0.1:4222`; deployment no longer
rewrites a private `4322` endpoint. Production configuration removes fields
whose values are owned by the Event Registry or EventBus topology. Test
fixtures may still start embedded NATS directly, but embedded broker settings
are not part of business-service configuration.

### Verification

The contract verifier runs all event-owning modules. Self-contained E2E tests
cover Storage fan-out through real module handlers and Strategy outbox through
the real Trade handler. The Trade E2E injects a retry/reordering scenario and
asserts that a stale command cannot create a second run. Factor tests prove
readiness drops during a failed session and recovers after reopening.
