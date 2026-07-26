# Event Interaction Naming and Layout Governance

## Status

Approved direction: lifecycle owner plus a fixed role vocabulary.

This design governs code that publishes, consumes, maps, stores, or transports
the five public MooX events. It changes names and ownership boundaries without
changing event behavior.

## Goals

1. Give every event interaction component one precise architectural role.
2. Place each component beside the business lifecycle that owns it.
3. Remove ambiguous package, file, and type names such as `bus`,
   `eventcontract`, `NATSConsumer`, and generic `event.go`.
4. Make the naming rules executable through repository checks.
5. Preserve the current EventMessage contracts, delivery semantics, ordering,
   recovery, and deployment boundaries.

## Non-Goals

- Add a generic event framework, generic Outbox/Inbox implementation, Saga,
  distributed transaction, or exactly-once guarantee.
- Move business behavior into `packages/events` or `packages/jetstream`.
- Change the five event names, versions, subjects, Streams, payload fields, or
  Consumer identities.
- Add compatibility aliases for old Go package paths or type names.

## Architectural Vocabulary

The following names have one meaning throughout the repository.

| Name | Meaning | Allowed ownership |
| --- | --- | --- |
| `events` | EventMessage, event Registry, semantic validation, typed publish and consume API | `packages/events` |
| `eventbus` | Broker process, Stream/KV topology, ACL, credentials, and control API | `modules/eventbus`, configuration fields, and operational commands |
| `jetstream` | Raw NATS JetStream transport, delivery, ACK decisions, Runner, and reconnect primitives | `packages/jetstream` and transport-specific implementation files |
| `outbox` | Locally committed outgoing messages and their relay lifecycle | The state owner that commits the Outbox |
| `inbox` | Durable incoming-message idempotency or pending-consumption state | The state owner that commits the Inbox |
| `eventpublisher` | Direct governed-event publishing adapter without local transactional ownership | The producing lifecycle owner |
| `eventconsumer` | Event binding, fetch, decode, dispatch, and ACK/RETRY/TERM lifecycle | The consuming lifecycle owner |
| `eventmapper` | Conversion between an owner-local model and a public event payload | The model owner |

Business modules must not use `bus` as a synonym for Outbox, Publisher,
Consumer, or JetStream. They must not use `eventcontract` for a mapper. The
public contract lives in `packages/events` and the public payload protobuf
package.

## Placement Rule

Physical uniformity follows ownership rather than a mandatory horizontal
`internal/eventing` layer.

Apply these rules in order:

1. Shared event semantics belong to `packages/events`.
2. Shared transport mechanics belong to `packages/jetstream`.
3. An EventBus implementation of an existing domain port stays beside that
   port. For example, CloudNode keeps `jobqueue.JetStreamQueue`, and Reporter
   keeps its event-backed reporting implementation.
4. A transactional producer belongs in the state owner's `outbox` package.
5. A standalone inbound adapter belongs in the consumer owner's
   `eventconsumer` package.
6. A direct producer without an Outbox belongs in the producer owner's
   `eventpublisher` package when it has an independent lifecycle. A small
   owner-local adapter may remain in the owner package as
   `event_publisher.go`.
7. Model conversion belongs in `eventmapper`.
8. Inbox persistence stays in the owning Store package unless Inbox behavior
   forms an independent lifecycle.

This rule permits paths such as:

```text
modules/strategy/internal/outbox
modules/storage/internal/service/datanode/outbox
modules/storage/internal/service/view/eventconsumer
modules/factor/internal/trigger/eventconsumer
modules/monitor/internal/hostmetrics/eventconsumer
modules/monitor/internal/metrics/eventconsumer
modules/trade/internal/eventconsumer
modules/archive/internal/eventconsumer
```

The shared suffix identifies the role. The prefix identifies the lifecycle
owner.

## File and Type Names

Role packages omit redundant role words from filenames:

```text
outbox/
  relay.go
  publisher.go
  runtime.go

eventconsumer/
  consumer.go
  handler.go
  delivery_policy.go
  jetstream.go

eventpublisher/
  publisher.go
  jetstream.go

eventmapper/
  rows.go
```

Transport names describe implementations, not business APIs. A public
`NATSConsumer` becomes `eventconsumer.Consumer`; its private JetStream session
may remain `jetStreamSession` in `jetstream.go`.

Use these type patterns:

- `outbox.Store`, `outbox.Publisher`, `outbox.Relay`, `outbox.Runtime`, and
  `outbox.RuntimeConfig`.
- `eventconsumer.Consumer`, `eventconsumer.Config`, and event-specific
  handlers such as `RebalanceHandler`.
- `eventpublisher.Publisher` and transport implementations such as
  `JetStreamPublisher`.
- Mapper functions named by direction, such as `ToEventRows` and
  `ToStorageRows`.

Outside a role package, a type must include its role: `DatasetEventPublisher`,
`RebalanceEventConsumer`, `OutboxMessage`, or `EventConsumerOptions`. Role
packages use owner-local message models rather than introducing a shared
Outbox or Inbox record.

Public protobuf packages remain named after business payload concepts rather
than delivery technology. `packages/events/eventpb` remains reserved for the
EventMessage envelope. This change will not mechanically add `event` to every
payload package name or move the existing payload packages.

## Target Layout

### Shared Packages and EventBus

`packages/events`, `packages/jetstream`, and `modules/eventbus` retain their
current ownership:

- `packages/events` owns the five-event Registry, EventMessage, semantic
  validation, subject derivation, and typed Publisher/Consumer API.
- `packages/jetstream` owns transport, delivery state, ACK decisions, Runner,
  and reconnect primitives.
- `modules/eventbus` owns Stream/KV topology, ACL, credentials, and management.

Business modules must not duplicate Registry-owned event names, subject
prefixes, or Stream names.

### Strategy

Move:

```text
modules/strategy/internal/bus
  -> modules/strategy/internal/outbox
```

Rename `outbox.go` to `relay.go`. Keep `publisher.go` and `runtime.go`.
Package-qualified names become `outbox.Relay`, `outbox.Runtime`, and
`outbox.JetStreamPublisher`.

Keep `internal/domain/outbox.go` and `internal/store/outbox.go`. They represent
the domain record and persistence implementation, not competing transport
packages.

### Storage Producer

Create:

```text
modules/storage/internal/service/datanode/outbox/
  publisher.go
  relay.go
  publisher_test.go
  relay_test.go
```

Move `OutboxRelay` from the `datanode` package into this package. Move
`DatasetPublisher` out of the current View `eventconsumer` directory and make
it the DataNode Outbox publisher.

Rename:

```text
modules/storage/internal/service/datanode/pebble/event.go
  -> modules/storage/internal/service/datanode/pebble/outbox_message.go
```

The Pebble package keeps message construction and Outbox-ID binding because
those operations participate in its atomic batch and key allocation. The
Outbox relay imports Pebble; Pebble must not import the relay package.

Delete the unused `DatasetPublisher.Publish` path, its ignored `nodeID`
argument, and the old subject helpers. Published subjects come only from the
Registry.

### Storage Mapper

Move:

```text
modules/storage/internal/eventcontract
  -> modules/storage/internal/eventmapper
```

Rename:

- `ToSharedRows` to `ToEventRows`.
- `ToLocalRows` to `ToStorageRows`.

The mapper continues to validate row identity at the boundary. It does not
define the contract.

### Storage View Consumer

Use the existing directory for the actual View consumer:

```text
modules/storage/internal/service/view/eventconsumer/
  consumer.go
  handler.go
  delivery_policy.go
  delivery_heartbeat.go
  subject_dispatcher.go
```

Move binding, fetch, subject-lane dispatch, heartbeat, and ACK policy into this
package. Define a narrow typed handler callback so the package does not import
the parent `view` package.

The parent View service keeps index application, backfill coordination,
missing-row recovery, and storage-specific writes. The consumer decodes the
delivery and passes the typed EventMessage and `DatasetRowsUpserted` payload to
the handler. A thin owner method wires that handler to the View service.

Delete `DatasetRowsUpsertedSubjectPrefix`,
`DatasetRowsUpsertedSubject`, and
`ParseDatasetRowsUpsertedSubject`. The Registry already renders and validates
the governed subject.

### Factor

Move the EventBus adapter out of the batching package:

```text
modules/factor/internal/trigger/nats.go
  -> modules/factor/internal/trigger/eventconsumer/
       consumer.go
       handler.go
       jetstream.go
```

Rename `NATSConsumer` to `eventconsumer.Consumer` and its transport config to
`eventconsumer.Config`. Keep `EventBatcher`, replay, and durable pending Inbox
behavior in `internal/trigger`.

Rename Factor configuration from `nats`/`NATSConfig` to
`eventbus`/`EventBusConfig`. URLs and credentials remain transport settings;
the business API no longer exposes NATS in its component name.

### Archive

Move:

```text
modules/archive/internal/consumer
  -> modules/archive/internal/eventconsumer
```

Keep `Decoder`, `Handler`, and `Runner`. Their current package exists solely to
consume governed Storage events, so the more specific package name removes
ambiguity without adding a layer.

### Trade

Create:

```text
modules/trade/internal/eventconsumer/
  rebalance.go
  rebalance_test.go
  jetstream.go
```

Move `runRebalanceConsumer` and `handleRebalanceDelivery` out of
`bootstrap/kernel_workers.go`. Bootstrap connects the client and starts the
consumer; the eventconsumer package owns binding, decode, ACK decisions, and
reconnect.

The `application/rebalance` package keeps request planning and domain
application. The Store keeps transactional Inbox persistence. The unrelated
`application/consumer` package is outside this event naming change; its
submission and fill workers do not consume governed EventMessages.

### Monitor

Split EventBus lifecycle from metric domain behavior:

```text
modules/monitor/internal/hostmetrics/eventconsumer/
  consumer.go
  consumer_test.go

modules/monitor/internal/metrics/eventconsumer/
  consumer.go
  consumer_test.go
```

The child packages import their parent domain packages. Parent packages must
not import their eventconsumer children. Bootstrap wires both.

Host validation, snapshot persistence, alert evaluation, metric storage,
authorization, and query behavior remain in `hostmetrics` and `metrics`.
Binding, decode, retry classification, and ACK decisions move to the child
packages. The child packages pass typed envelopes and payloads to parent
handlers; parent domain packages no longer accept `jetstream.Delivery` or
return `jetstream.HandlerResult`.

Remove duplicated public Topic and Stream variables when the Registry already
owns them. Keep code-owned Consumer names as constants in the consumer package.

### HostAgent

Split the direct governed-event publisher from Agent orchestration:

```text
modules/hostagent/internal/eventpublisher/
  publisher.go
  jetstream.go
  publisher_test.go
```

The Agent owns collection cadence and status. The publisher owns Registry
encoding, EventBus connection, publish, readiness, and close behavior.

### Domain-Port Exceptions

CloudNode `jobqueue.JetStreamQueue`, Collector task execution, and
`packages/report` remain beside their domain ports. Their EventBus use is an
implementation detail of `JobQueue`, task execution, or reporting.

These exceptions still follow implementation naming:

- Transport files use `jetstream_*.go`.
- Exported types describe the domain port and implementation, such as
  `JetStreamQueue`.
- Raw subject construction remains forbidden when the Registry provides the
  route.

## Data Flow and Error Boundaries

### Transactional Producer

```text
business transaction
  -> persist EventMessage in owner Outbox
  -> Outbox Relay loads pending message
  -> Outbox publisher publishes through packages/events
  -> JetStream PubAck succeeds
  -> owner deletes or marks the Outbox message complete
```

A publish failure retains the Outbox record. A reconnect restores publication.
The rename must not change ordering, stable event IDs, or duplicate PubAck
handling.

### Consumer

```text
packages/events Consumer
  -> eventconsumer decodes and validates EventMessage
  -> typed owner handler commits business state or Inbox
  -> eventconsumer returns ACK, RETRY, or TERM
```

Malformed or semantically invalid messages terminate. Transient transport,
storage, or dependency failures retry. Business state commits before ACK.
Only eventconsumer and transport packages handle `jetstream.Delivery` and
`jetstream.HandlerResult`; owner handlers receive typed envelopes and payloads.

### Mapper

```text
owner-local model
  <-> eventmapper
  <-> public protobuf payload
```

The mapper validates identity and required data in both directions. It does
not render subjects, choose Streams, publish messages, or own retries.

## Preserved Behavioral Invariants

The migration must preserve:

1. The five code-first Event definitions and seven-field EventMessage.
2. Registry-owned subject rendering and semantic identity validation.
3. Storage Outbox atomicity, contiguous-prefix deletion, and per-Dataset
   publication order.
4. Storage View same-subject order, cross-subject concurrency, queued
   heartbeats, retry/TERM policy, and backfill write priority.
5. Factor five-dimensional batching key, fixed first-event window, durable
   pending Inbox, replay, and reconnect readiness.
6. Archive journal sync before ACK and quarantine/retry behavior.
7. Strategy stable Outbox IDs, reconnect catch-up, and readiness reporting.
8. Trade Inbox idempotency, command sequence checks, and atomic rebalance plan
   creation.
9. Monitor producer authorization, storage-before-ACK behavior, bounded retry,
   and alert processing.
10. CloudNode exact Subject Consumer ownership and domain queue behavior.

## Executable Governance

Extend `scripts/check-package-boundaries.sh` to reject:

- `modules/*/internal/bus` for business event interaction.
- Any `package eventcontract`.
- Exported `NATSConsumer` and transport-named business Consumer APIs.
- The removed Storage subject helpers and old role paths.
- Publisher declarations under an `eventconsumer` package.
- Owner-domain handlers that accept `jetstream.Delivery` or return
  `jetstream.HandlerResult` after their eventconsumer adapter has been split.
- Active documentation that presents old paths as current architecture.

Update `scripts/verify-event-contracts.sh` to run the renamed packages and
retain its current semantic checks. Add checks that Registry-owned event names,
subjects, and Streams are not duplicated in business adapters.

Update current architecture documents, module READMEs, and test commands. Dated
execution plans remain historical records and need not be rewritten.

## Verification

Verification scales with the cross-module blast radius:

1. Run unit tests for every moved package.
2. Run race tests for Strategy Outbox, Storage Outbox/View consumer, Factor
   consumer/Inbox, Archive consumer, Trade consumer/Inbox, and Monitor
   consumers.
3. Run `scripts/check-package-boundaries.sh`.
4. Run `scripts/verify-event-contracts.sh`.
5. Run the Storage-to-View/Factor/Archive event contract tests.
6. Run Strategy-to-Trade Outbox/Inbox E2E.
7. Run HostAgent-to-Monitor and service-metrics-to-Monitor E2E.
8. Run CloudNode exact-subject queue tests to prove the exception remains
   governed by the Registry.
9. Run `./scripts/test-go-workspace.sh` and `make verify-pr`.
10. Run `git diff --check` and verify the worktree and remote branch at the
    final commit.

## Migration Policy

This is a new project. The implementation moves callers in one change and
deletes old packages, aliases, compatibility wrappers, build tags, and
duplicate APIs. No dual package paths, deprecated aliases, or transitional
configuration keys remain.
