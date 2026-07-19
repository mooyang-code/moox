# MooX EventBus Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `moox-eventbus` as the single MooX-owned NATS JetStream service, define the common `MooxMessage` wire contract, provide a reusable JetStream client package, and migrate Storage, CloudNode, and existing consumers away from private embedded brokers.

**Architecture:** `modules/eventbus` embeds `nats-server/v2` and owns broker lifecycle, Stream/KV/declared-consumer reconciliation, health, metrics, and a read-only tRPC management plane. Business processes connect directly to its NATS port through `packages/jetstream`; there is no tRPC/HTTP publish proxy. Every MooX application message is an individually acknowledged protobuf `MooxMessage`, while domain payloads remain opaque and versioned by Topic. Public Host Agent connections use private-CA TLS and role-scoped credentials; Host Agent publication is best effort before PubAck, while EventBus-to-Monitor delivery remains durable and at least once after PubAck.

**Tech Stack:** Go 1.24, tRPC-Go, Protocol Buffers, `github.com/nats-io/nats-server/v2` v2.11.3, `github.com/nats-io/nats.go` v1.47.0, Prometheus client library, existing Admin gateway/SysDeploy conventions, and existing build/release scripts.

---

## Reading Summary

- `modules/storage` currently has a private transport abstraction, a JetStream producer/subscriber, optional embedded NATS startup, and row-change subjects under `moox.storage.>`.
- `modules/cloudnode` owns a second JetStream runtime and optional embedded server on a different port; it uses a WorkQueue Stream plus a JetStream KV bucket.
- `modules/factor` directly imports `nats.go` to subscribe to Storage messages.
- There is no public NATS package under `packages/`, so connection, Stream reconciliation, ACK, retry, and shutdown behavior are duplicated.
- `go.work` treats each module and shared package as a separate Go module. The new protocol, client package, EventBus service, and generated tRPC package must each be wired explicitly.
- The existing Storage write path commits PrimaryStore data before publishing its event. This plan replaces that non-atomic gap with a local durable outbox on each PrimaryStore shard and an asynchronous relay to the central EventBus.
- The existing `docs/superpowers/plans/2026-07-08-storage-view-write-journal-materialization.md` still governs View hot-path materialization. This plan owns the common message protocol, central JetStream topology, Storage outbox, and transport migration; it must preserve that plan's `RowsUpdated` payload semantics.
- `docs/superpowers/plans/2026-07-10-host-agent-admin-secrets-resource-monitoring.md` is the companion Host Agent plan. It owns the `HostMetric` payload, Linux collectors, Admin `t_secrets` material, Monitor projection, and Skill-driven deployment; this plan owns their EventBus Topic, TLS, ACL, Stream, and durable-consumer prerequisites.

## Locked Decisions

| # | Decision |
|---|---|
| D1 | The internal protocol is solely **MooX Message Protocol v1**; core code, configuration, and documentation use MooX protocol terminology. |
| D2 | The single common protobuf type is `MooxMessage`; do not introduce an alternate wrapper type. |
| D3 | `MooxMessage` does not contain `resource_key`, `partition_key`, `correlation_id`, or `causation_id`. Business identifiers stay in domain payloads; transport ordering is a `PublishOption`. |
| D4 | `topic` is both the concrete NATS Subject and the versioned payload contract. One Topic major version maps to exactly one payload schema. The registry may declare a versioned Topic family pattern for routed subjects, but every `MooxMessage.topic` still equals its concrete publish Subject. |
| D5 | Services publish directly to the NATS listener embedded in `moox-eventbus` through `packages/jetstream`. There is no generic or Host-Agent-specific Publish RPC/HTTP endpoint; Admin exposes read-only EventBus management only. |
| D6 | `modules/eventbus` is the only production owner of `nats-server/v2`; Storage and CloudNode stop starting embedded servers. Tests may start an in-process NATS server. |
| D7 | `modules/eventbus` owns Stream, KV, and declared durable-consumer definitions. Business clients may publish and bind or create only consumers explicitly allowed by the registry and ACL; they may not mutate Stream/KV retention configuration. |
| D8 | Messages accepted by JetStream and consumer redelivery are at least once. Retrying publishers reuse `message_id`, and consumers remain idempotent beyond the broker deduplication window. Host Agent is the explicit publisher-side exception: one bounded attempt per sample, no outbox/replay, and drop on failure. |
| D9 | Batch throughput uses asynchronous publication of multiple independent NATS messages. Do not combine unrelated logical messages into a transport-level batch with one ACK. |
| D10 | Callback subscriptions are not implemented in V1. `MooxMessage`, durable pull consumers, and the management model must allow a future callback-delivery worker without changing publishers. |
| D11 | V1 production runs one standalone `moox-eventbus` node with one Stream replica. Configuration keeps a future cluster shape, but non-loopback cluster routes fail validation until dedicated route TLS/auth and per-node certificates are implemented in a later plan; client and message protocols will not need to change. |
| D12 | Host resource samples use exactly `moox.metrics.host.reported.v1`; it maps only to `trpc.moox.hostagent.HostMetric` and belongs to `MOOX_METRICS`. |
| D13 | Any NATS listener reachable beyond loopback requires TLS and authentication. Production uses a long-lived private CA, a server certificate containing every advertised public IP plus `127.0.0.1`/`::1` in IP SAN, and normal certificate verification. Plaintext public NATS, insecure verification, TOFU, and server-name overrides are forbidden. |
| D14 | Host monitoring uses two separate roles: all Host Agents share NATS user `hostagent-publisher` and one `eventbus_token`; Monitor uses `monitor-hostmetrics-consumer` and `monitor_eventbus_token`. Publisher can only publish the fixed Host Topic and receive PubAck; Monitor can only pull/ACK its fixed durable and publish DLQ. Storage, CloudNode, and Factor use the four separate identities and ACLs locked by their migration tasks below. |
| D15 | Deployment creates all six role tokens and both TLS bundles through the single Admin credential CLI, stores their encrypted values in `t_secrets`, and renders deployment-user-owned runtime files. Normal upgrades reuse them; release archives and checked-in YAML contain no credential or private key. |
| D16 | Host Agent never persists unsent samples and never replays old samples. Storage's transactional outbox is a separate business guarantee and remains unchanged. |
| D17 | Production users file also contains isolated `eventbus-internal-admin`, `storage-eventbus`, `cloudnode-eventbus`, and `factor-eventbus` identities. Each has one permanent token in Admin `t_secrets` and only the subjects/JetStream APIs needed by its owner; business services never receive the internal-admin token. |

## Protocol Contract

```protobuf
syntax = "proto3";

package trpc.moox.message;

import "google/protobuf/timestamp.proto";

option go_package = "github.com/mooyang-code/moox/packages/messagepb;messagepb";

enum MessageKind {
  MESSAGE_KIND_UNSPECIFIED = 0;
  MESSAGE_KIND_EVENT = 1;
  MESSAGE_KIND_COMMAND = 2;
  MESSAGE_KIND_SNAPSHOT = 3;
}

message Producer {
  string service_name = 1;
  string instance_id = 2;
  string node_id = 3;
  string boot_id = 4;
  string version = 5;
}

message TraceContext {
  string trace_id = 1;
  string request_id = 2;
}

message MooxMessage {
  uint32 protocol_version = 1;
  string message_id = 2;
  string topic = 3;
  MessageKind kind = 4;
  Producer producer = 5;
  string space_id = 6;
  uint64 sequence = 7;
  google.protobuf.Timestamp occurred_at = 8;
  google.protobuf.Timestamp published_at = 9;
  string content_type = 10;
  bytes payload = 11;
  TraceContext trace = 12;
  map<string, string> attributes = 13;
}

message RejectedMessage {
  string source_stream = 1;
  uint64 source_stream_sequence = 2;
  string source_topic = 3;
  string source_message_id = 4;
  string reason_code = 5;
  string reason = 6;
  google.protobuf.Timestamp rejected_at = 7;
  string payload_sha256 = 8;
}
```

### Field Semantics

- `protocol_version` is the outer MooX Message protocol version and is `1` for this plan.
- `message_id` is globally unique, stable across retries, and copied to the `Nats-Msg-Id` header.
- `topic` must exactly equal the concrete NATS Subject used for publication.
- `content_type` describes `payload`, not the outer NATS body. Protobuf payloads use `application/x-protobuf; message=<fully-qualified-proto-name>`.
- The NATS `Content-Type` header describes the outer message and is fixed to `application/vnd.moox.message+protobuf`.
- `sequence` increases within `producer.service_name + producer.instance_id + producer.boot_id`; zero means the producer does not supply gap detection.
- `occurred_at` records the business occurrence. `published_at` records the first relay publication attempt and is not rewritten on retry.
- `attributes` is limited to routing-neutral, low-cardinality metadata. Secrets and complete business objects are forbidden.

### Topic Naming

```text
moox.<domain>.<entity>.<action>.v<major>
```

Initial Topic catalog:

```text
moox.storage.time_series.rows_updated.v1
moox.storage.record.rows_updated.v1
moox.metrics.host.reported.v1
moox.cloudnode.exec.v1.jobitem.s.<space>.pkg.<package>.type.<job_type>
moox.dlq.message.rejected.v1
```

`moox.metrics.host.reported.v1` has kind `MESSAGE_KIND_SNAPSHOT` and payload content type `application/x-protobuf; message=trpc.moox.hostagent.HostMetric`. A different metrics payload uses a different versioned Topic; it must not reuse the Host Topic.

`moox.storage.time_series.rows_updated.v1` and `moox.storage.record.rows_updated.v1` have kind `MESSAGE_KIND_EVENT` and payload types `trpc.moox.storage.TimeSeriesRowsUpdated` and `trpc.moox.storage.RecordRowsUpdated`. View Builder and Factor bind independent predeclared durables; they do not create consumers at startup. V1 标准部署不启动独立 Archive consumer；以后启用时必须先在 EventBus registry 增加两个 exact durables，不能复用 View Builder durable。

`moox.dlq.message.rejected.v1` has kind `MESSAGE_KIND_EVENT` and payload content type `application/x-protobuf; message=trpc.moox.message.RejectedMessage`. Its bounded `reason` is sanitized, and it records only the source payload SHA-256 rather than copying the rejected raw payload.

The `moox.cloudnode.exec.v1.jobitem.s.*.pkg.*.type.*` Topic family has kind `MESSAGE_KIND_COMMAND` and one `JobItem` payload schema. Space/package/job-type tokens remain in the concrete Subject so WorkQueue consumers keep non-overlapping exact filters and durable names.

Compatible protobuf field additions retain the Topic version. Field renumbering, meaning changes, or incompatible payload replacement require a new `.v2` Topic and a parallel migration period.

## JetStream Topology

| Stream | Subjects | Retention | Default limits |
|---|---|---|---|
| `MOOX_STORAGE` | `moox.storage.>` | Limits/File/DiscardOld | 72h, 20 GiB |
| `MOOX_METRICS` | `moox.metrics.>` | Limits/File/DiscardOld | 24h, 10 GiB |
| `MOOX_CLOUDNODE_EXEC` | `moox.cloudnode.exec.v1.>` | WorkQueue/File/DiscardOld | 72h, 10 GiB |
| `MOOX_DLQ` | `moox.dlq.>` | Limits/File/DiscardOld | 30d, 2 GiB |

`MOOX_CLOUDNODE_JOB_ACTIVE` remains a JetStream KV bucket with one value per key and a configurable 48h TTL. These defaults are explicit YAML values, not hidden constants, and production sizing is changed through EventBus configuration only.

## Target File Map

### Common Message Protocol

- Create `packages/messagepb/go.mod`, `Makefile`, `moox_message.proto`, generated `moox_message.pb.go`, and `contract_test.go`.
- Modify `go.work` and root `Makefile` to include protocol generation and tests.

### Shared JetStream Client

- Create `packages/jetstream/go.mod`.
- Create `packages/jetstream/config.go`, `client.go`, `publisher.go`, `consumer.go`, `delivery.go`, `codec.go`, `errors.go`, and focused tests.
- The package imports `nats.go` and `packages/messagepb`; it does not import `nats-server/v2`.

### EventBus Service

- Create `modules/eventbus/go.mod`, `cmd/server/main.go`, `config/app.yaml`, and `config/trpc_go.yaml`.
- Create `modules/eventbus/internal/config`, `broker`, `registry`, `management`, `health`, and `bootstrap` packages.
- Create `modules/eventbus/proto/eventbus.proto`, `Makefile`, and generated module `modules/eventbus/proto/eventbusgen`.
- Create `modules/eventbus/README.md`.

### Existing Module Migration

- Modify Storage's message proto, eventbus bootstrap, PrimaryStore write path, Pebble store, and split-role configuration.
- Modify CloudNode's JetStream bootstrap and queue to use `packages/jetstream` and the central endpoint.
- Modify Factor's Storage trigger consumer to use `packages/jetstream`.
- Remove private embedded NATS implementations and direct `nats.go` imports after migration tests pass.

### Product And Operations

- Modify `modules/admin/proto/sysdeploy_service.proto`, `modules/admin/internal/service/sysdeploy/defaults.go`, `modules/admin/internal/service/sysdeploy/defaults_test.go`, `scripts/build.sh`, `scripts/release.sh`, `scripts/deploy-moox.sh`, root `Makefile`, `go.work`, `modules/README.md`, and root `README.md`.
- Modify Admin EventBus credential provisioning from the companion Host Agent plan and add `skills/moox/scripts/eventbus-credentials.sh`; release artifacts remain credential-free.
- Add deployment regression coverage for `moox-eventbus`, its data directory, and disabled-component preservation.

## Delivery Order

| Milestone | Tasks | Exit condition |
|---|---|---|
| M1 Protocol and client | 1-2 | A validated `MooxMessage` can be published, fetched, ACKed, retried, and deduplicated against a test JetStream. |
| M2 EventBus service | 3-6 | The service starts its broker, reconciles configured resources, reports readiness, and exposes read-only tRPC status. |
| M3 Packaging | 7 | `moox-eventbus` is built, released, deployed, registered in SysDeploy, and survives restart with persisted JetStream data. |
| M4 Storage migration | 8-9 | PrimaryStore commits data plus outbox atomically and relays versioned Storage messages asynchronously to the central EventBus. |
| M5 Remaining migration | 10-11 | CloudNode and Factor use the central EventBus; production modules contain no embedded NATS server. |
| M6 Verification | 12 | Failure, restart, load, dedupe, and deployment checks pass end to end. |

Cross-plan order is fixed: EventBus Tasks 1-6 -> Host companion Tasks 2-3 -> EventBus Task 7 -> Host companion Task 4 gate. EventBus Tasks 1-6 own all EventBus/shared-package source and do not depend on Host credential code. Task 7 has a hard dependency only on Host Task 3, which is the single owner of the Admin credential DAO/service/CLI, all six role-token records, and both TLS bundles; Task 7 invokes it and must not create a second provisioning path. EventBus Tasks 8-12 then continue from Task 7 under this plan's ownership.

---

### Task 1: Add The MooX Message Protocol

**Files:**
- Create: `packages/messagepb/go.mod`
- Create: `packages/messagepb/Makefile`
- Create: `packages/messagepb/moox_message.proto`
- Generate: `packages/messagepb/moox_message.pb.go`
- Create: `packages/messagepb/contract_test.go`
- Modify: `go.work`
- Modify: `Makefile`

- [ ] **Step 1: Write the descriptor contract test**

Create a test that loads `(&messagepb.MooxMessage{}).ProtoReflect().Descriptor()` and asserts field numbers/names, `MessageKind` values, and the absence of `resource_key`, `partition_key`, `correlation_id`, and `causation_id`. Add a second descriptor assertion for all eight `RejectedMessage` fields and verify it has no raw-payload field.

```go
func TestMooxMessageContract(t *testing.T) {
    d := (&MooxMessage{}).ProtoReflect().Descriptor()
    want := map[protoreflect.Name]protoreflect.FieldNumber{
        "protocol_version": 1, "message_id": 2, "topic": 3,
        "kind": 4, "producer": 5, "space_id": 6, "sequence": 7,
        "occurred_at": 8, "published_at": 9, "content_type": 10,
        "payload": 11, "trace": 12, "attributes": 13,
    }
    for name, number := range want {
        if field := d.Fields().ByName(name); field == nil || field.Number() != number {
            t.Fatalf("field %s number mismatch", name)
        }
    }
}
```

- [ ] **Step 2: Define the complete protobuf contract**

Write `moox_message.proto` exactly as shown in **Protocol Contract**, including `RejectedMessage`. Do not add `google.protobuf.Any`; the EventBus treats domain payloads as opaque bytes.

- [ ] **Step 3: Add generation and workspace wiring**

Use the existing `packages/commonpb/Makefile` as the command pattern. Add `./packages/messagepb` to `go.work` and a root Makefile target that runs generation from the package directory.

- [ ] **Step 4: Generate and verify**

Run:

```bash
make -C packages/messagepb
go test -count=1 ./packages/messagepb
```

Expected: generated Go code compiles and `TestMooxMessageContract` passes.

- [ ] **Step 5: Commit**

```bash
git add go.work Makefile packages/messagepb
git commit -m "feat(eventbus): define moox message protocol"
```

---

### Task 2: Build The Shared JetStream Client

**Files:**
- Create: `packages/jetstream/go.mod`
- Create: `packages/jetstream/config.go`
- Create: `packages/jetstream/client.go`
- Create: `packages/jetstream/codec.go`
- Create: `packages/jetstream/publisher.go`
- Create: `packages/jetstream/consumer.go`
- Create: `packages/jetstream/delivery.go`
- Create: `packages/jetstream/kv.go`
- Create: `packages/jetstream/errors.go`
- Create: `packages/jetstream/client_test.go`
- Create: `packages/jetstream/publisher_test.go`
- Create: `packages/jetstream/consumer_test.go`
- Create: `packages/jetstream/delivery_test.go`
- Create: `packages/jetstream/kv_test.go`
- Modify: `go.work`

- [ ] **Step 1: Write validation and codec tests**

Test rejection of protocol version zero, empty IDs, invalid Topics, Topic/Subject mismatch, missing producer identity, missing timestamps, missing payload content type, and oversized payloads. Test deterministic protobuf round trips and verify decoding copies byte slices before returning them.

- [ ] **Step 2: Define the public API**

```go
type Config struct {
    URLs                 []string
    Name                 string
    Username             string
    Password             string
    Credentials          string
    TLSCAFile            string
    TLSCertFile          string
    TLSKeyFile           string
    ConnectTimeout       time.Duration
    ReconnectWait        time.Duration
    MaxReconnects        int
    ReconnectBufferBytes int
    MaxPayload           int
}

type PublishOption func(*publishOptions)

func WithOrderingKey(key string) PublishOption

type PublishAck struct {
    Stream    string
    Sequence  uint64
    Duplicate bool
}

type ConsumerRef struct {
    Stream  string
    Durable string
}

type KVEntry struct {
    Value    []byte
    Revision uint64
}

type KVStore interface {
    Create(ctx context.Context, key string, value []byte) (uint64, error)
    Get(ctx context.Context, key string) (*KVEntry, error)
    Update(ctx context.Context, key string, value []byte, revision uint64) (uint64, error)
    Keys(ctx context.Context) ([]string, error)
}

func Connect(ctx context.Context, cfg Config) (*Client, error)
func (c *Client) Publish(ctx context.Context, msg *messagepb.MooxMessage, opts ...PublishOption) (*PublishAck, error)
func (c *Client) PublishBatch(ctx context.Context, messages []*messagepb.MooxMessage, opts ...PublishOption) []PublishResult
func (c *Client) BindPullConsumer(ctx context.Context, ref ConsumerRef) (*PullConsumer, error)
func (c *Client) EnsurePullConsumer(ctx context.Context, cfg ConsumerConfig) (*PullConsumer, error)
func (c *Client) BindKV(ctx context.Context, bucket string) (KVStore, error)
func (c *Client) AckToken(ctx context.Context, token string) error
func (c *Client) NakToken(ctx context.Context, token string, delay time.Duration) error
func (c *Client) InProgressToken(ctx context.Context, token string) error
func (c *Client) TermToken(ctx context.Context, token string) error
func (c *Client) Close() error
```

`PublishBatch` must issue independent async JetStream publications and return one result per input message in input order. It must not create a transport batch with one ACK.

`BindPullConsumer` is strictly bind-only: it returns a typed not-found/config-mismatch error and never creates or updates a consumer. Monitor uses this method for `monitor_hostmetrics_ingest_v1`, so its token needs no consumer-create permission. `EnsurePullConsumer` is reserved for callers whose registry and ACL explicitly permit consumer creation. `BindKV` only opens an existing bucket and exposes the four operations CloudNode currently needs; it never creates or reconfigures KV.

Exactly one authentication form is configured. Host Agent uses username `hostagent-publisher`; Monitor uses `monitor-hostmetrics-consumer`. Their permanent tokens are passed as `Password` from a `0600` credential file, never through command arguments. Non-loopback URLs require `tls://` plus `TLSCAFile`; certificate verification cannot be disabled. Host Agent sets `ReconnectBufferBytes=0`, so disconnected samples cannot become a hidden replay queue.

- [ ] **Step 3: Implement message transport mapping**

Set:

```text
NATS Subject: msg.topic
Nats-Msg-Id: msg.message_id
Content-Type: application/vnd.moox.message+protobuf
Moox-Ordering-Key: PublishOption value, when non-empty
```

Validate before marshal, use JetStream PubAck, preserve the original `message_id` on retry, and return typed errors for validation, connection, publish timeout, and decode failures.

- [ ] **Step 4: Implement durable pull delivery**

Expose explicit delivery control:

```go
type Delivery struct {
    Message      *messagepb.MooxMessage
    Subject      string
    Stream       string
    Consumer     string
    StreamSeq    uint64
    ConsumerSeq  uint64
    DeliveryCount uint64
    PersistentToken string
}

func (d *Delivery) Ack(ctx context.Context) error
func (d *Delivery) Nak(ctx context.Context, delay time.Duration) error
func (d *Delivery) InProgress(ctx context.Context) error
func (d *Delivery) Term(ctx context.Context) error
```

`Fetch` returns deliveries without auto-ACK. Domain code decides when durable work has completed.

`PersistentToken` is an opaque, bounded encoding of the broker ACK subject plus parsed stream/consumer identity. CloudNode may persist it in its existing active-job KV and call the client-level token methods after an RPC round trip or process reconnect. Token methods must parse strictly, reject a stream/consumer mismatch inside the encoded value, reject non-`$JS.ACK` subjects and oversize/control-character input, and then rely on the role ACL for final authorization. Normal in-process consumers use the `Delivery` methods instead.

- [ ] **Step 5: Implement bind-only KV and restart-safe token control**

Map not-found, key-exists, revision-conflict, timeout, and permission errors to typed package errors; copy all returned byte slices. Test KV Create/Get/Update/Keys CAS behavior. Fetch a message, persist `PersistentToken`, close/reconnect the client, ACK through `AckToken`, and prove the message is not redelivered; repeat NAK, InProgress, Term, malformed token, wrong stream/consumer, and unauthorized role cases.

- [ ] **Step 6: Add integration tests with a test-only embedded server**

Cover PubAck, `Nats-Msg-Id` duplicate detection, redelivery after NAK, redelivery after `AckWait`, `AckSync`, reconnect, disabled reconnect buffering, malformed body rejection, and batch partial failure. Prove a token without consumer-create permission can `BindPullConsumer`/fetch/ACK an existing durable and cannot create a missing one. Add private-CA tests for correct public IP SAN, wrong IP SAN, unknown CA, and expired certificate. The embedded NATS dependency belongs in test imports only.

- [ ] **Step 7: Run tests and commit**

```bash
go test -count=1 ./packages/messagepb ./packages/jetstream
git add go.work packages/jetstream
git commit -m "feat(eventbus): add shared jetstream client"
```

---

### Task 3: Define The EventBus Management Protocol

**Files:**
- Create: `modules/eventbus/proto/eventbus.proto`
- Create: `modules/eventbus/proto/Makefile`
- Create: `modules/eventbus/proto/eventbusgen/go.mod`
- Generate: `modules/eventbus/proto/eventbusgen/eventbus.pb.go`
- Generate: `modules/eventbus/proto/eventbusgen/eventbus.trpc.go`
- Create: `modules/eventbus/proto/eventbusgen/contract_test.go`
- Modify: `go.work`

- [ ] **Step 1: Write a service descriptor test**

Lock the service name `trpc.moox.eventbus.EventBusMgr` and methods `GetOverview`, `ListTopics`, `ListStreams`, `ListConsumers`, and `GetConsumer`. Assert that no method named `Publish`, `Send`, or `Produce` exists.

- [ ] **Step 2: Define read-only management messages**

The proto must use `common.RetInfo` and `common.Page/PageResult`, and expose:

```protobuf
message TopicInfo { string topic = 1; string stream = 2; MessageKind kind = 3; string payload_content_type = 4; uint32 payload_version = 5; bool enabled = 6; }
message StreamInfo { string name = 1; repeated string subjects = 2; string retention = 3; string storage = 4; uint64 messages = 5; uint64 bytes = 6; string first_time = 7; string last_time = 8; }
message ConsumerInfo { string stream = 1; string name = 2; string filter_subject = 3; uint64 pending = 4; uint64 ack_pending = 5; uint64 redelivered = 6; string last_delivered_at = 7; }
message Overview { bool jetstream_ready = 1; uint32 connections = 2; uint32 streams = 3; uint32 consumers = 4; uint64 messages = 5; uint64 bytes = 6; uint64 total_pending = 7; }
```

Use `messagepb.MessageKind` rather than defining a second enum.

- [ ] **Step 3: Generate, test, and commit**

```bash
make -C modules/eventbus/proto
go test -count=1 ./modules/eventbus/proto/eventbusgen
git add go.work modules/eventbus/proto
git commit -m "feat(eventbus): define management protocol"
```

---

### Task 4: Add EventBus Configuration And Broker Lifecycle

**Files:**
- Create: `modules/eventbus/go.mod`
- Create: `modules/eventbus/config/app.yaml`
- Create: `modules/eventbus/config/trpc_go.yaml`
- Create: `modules/eventbus/internal/config/config.go`
- Create: `modules/eventbus/internal/config/config_test.go`
- Create: `modules/eventbus/internal/broker/server.go`
- Create: `modules/eventbus/internal/broker/server_test.go`
- Create: `modules/eventbus/cmd/server/main.go`
- Modify: `go.work`

- [ ] **Step 1: Write configuration tests**

Cover defaults, YAML overrides, environment overrides, duplicate Stream names, overlapping exact Topics/Topic families, invalid Topic versions, routed family token counts, Stream subject coverage, unsafe root StoreDir, incomplete TLS configuration, and authentication enabled without credentials. Assert the complete concrete-consumer set is exactly two Storage View Builder durables, `factor_calc`, and `monitor_hostmetrics_ingest_v1`, plus the CloudNode template. Add fail-closed cases for undeclared durable bindings, non-loopback bind without TLS/auth, users file not owned by the process or not `0600`, symlinks, wrong/expired IP SAN, duplicate roles, and ACLs broader than their fixed contract.

- [ ] **Step 2: Add explicit default configuration**

```yaml
broker:
  host: 127.0.0.1
  port: 4222
  client_advertise: ""
  server_name: eventbus-dev-1
  store_dir: ./data/eventbus/jetstream
  startup_timeout: 10s
  max_payload_bytes: 8388608
  cluster:
    enabled: false
    name: MOOX_EVENTBUS
    host: 127.0.0.1
    port: 6222
    routes: []
  auth:
    enabled: false
    users_file: ""
  tls:
    enabled: false
    cert_file: ""
    key_file: ""
    ca_file: ""

internal_client:
  credential_file: ""
  tls_ca_file: ""

health:
  addr: "127.0.0.1:11419"

streams:
  - name: MOOX_STORAGE
    subjects: ["moox.storage.>"]
    retention: limits
    replicas: 1
    max_age: 72h
    max_bytes: 21474836480
  - name: MOOX_METRICS
    subjects: ["moox.metrics.>"]
    retention: limits
    replicas: 1
    max_age: 24h
    max_bytes: 10737418240
  - name: MOOX_CLOUDNODE_EXEC
    subjects: ["moox.cloudnode.exec.v1.>"]
    retention: work_queue
    replicas: 1
    max_age: 72h
    max_bytes: 10737418240
  - name: MOOX_DLQ
    subjects: ["moox.dlq.>"]
    retention: limits
    replicas: 1
    max_age: 720h
    max_bytes: 2147483648
```

Add the Topic registry, declared durable consumers, and CloudNode KV bucket in the same file with explicit values. At minimum:

```yaml
topics:
  - topic: moox.storage.time_series.rows_updated.v1
    stream: MOOX_STORAGE
    kind: event
    payload_content_type: application/x-protobuf; message=trpc.moox.storage.TimeSeriesRowsUpdated
    payload_version: 1
    enabled: true
  - topic: moox.storage.record.rows_updated.v1
    stream: MOOX_STORAGE
    kind: event
    payload_content_type: application/x-protobuf; message=trpc.moox.storage.RecordRowsUpdated
    payload_version: 1
    enabled: true
  - topic: moox.metrics.host.reported.v1
    stream: MOOX_METRICS
    kind: snapshot
    payload_content_type: application/x-protobuf; message=trpc.moox.hostagent.HostMetric
    payload_version: 1
    enabled: true
  - topic: moox.dlq.message.rejected.v1
    stream: MOOX_DLQ
    kind: event
    payload_content_type: application/x-protobuf; message=trpc.moox.message.RejectedMessage
    payload_version: 1
    enabled: true

topic_families:
  - pattern: moox.cloudnode.exec.v1.jobitem.s.*.pkg.*.type.*
    stream: MOOX_CLOUDNODE_EXEC
    kind: command
    payload_content_type: application/x-protobuf; message=trpc.moox.cloudnode.JobItem
    payload_version: 1
    enabled: true

consumers:
  - stream: MOOX_STORAGE
    durable: storage_view_builder_time_series_rows_updated_v1
    filter_subject: moox.storage.time_series.rows_updated.v1
    ack_policy: explicit
    deliver_policy: all
    replay_policy: instant
    ack_wait: 120s
    max_ack_pending: 128
    max_deliver: -1
  - stream: MOOX_STORAGE
    durable: storage_view_builder_record_rows_updated_v1
    filter_subject: moox.storage.record.rows_updated.v1
    ack_policy: explicit
    deliver_policy: all
    replay_policy: instant
    ack_wait: 120s
    max_ack_pending: 128
    max_deliver: -1
  - stream: MOOX_STORAGE
    durable: factor_calc
    filter_subject: moox.storage.time_series.rows_updated.v1
    ack_policy: explicit
    deliver_policy: new
    replay_policy: instant
    ack_wait: 60s
    max_ack_pending: 1000
    max_deliver: 5
  - stream: MOOX_METRICS
    durable: monitor_hostmetrics_ingest_v1
    filter_subject: moox.metrics.host.reported.v1
    ack_policy: explicit
    deliver_policy: all
    replay_policy: instant
    ack_wait: 60s
    max_ack_pending: 256
    max_deliver: -1

consumer_templates:
  - stream: MOOX_CLOUDNODE_EXEC
    durable_prefix: cn_exec_
    filter_pattern: moox.cloudnode.exec.v1.jobitem.s.*.pkg.*.type.*
    ack_policy: explicit
    deliver_policy: all
    replay_policy: instant
    ack_wait: 60s
    max_ack_pending: 256
    max_deliver: -1
```

`storage-eventbus` may publish only the two exact Storage Topics, receive PubAck replies, and pull/ACK only the two `storage_view_builder_*` durables above. `factor-eventbus` may pull/ACK only `factor_calc`. Both are bind-only and have no consumer create/update/delete, Stream, KV, Host, CloudNode, or DLQ permission. `eventbus-internal-admin` remains the only identity that reconciles concrete declared consumers.

`cloudnode-eventbus` is the one deliberate wider business role: it may publish `moox.cloudnode.exec.v1.>`, receive PubAck, manage consumers only in `MOOX_CLOUDNODE_EXEC`, pull/ACK those consumers, and access only `MOOX_CLOUDNODE_JOB_ACTIVE` KV. Static NATS users-file ACL can isolate the Stream/bucket but cannot inspect a legacy consumer-create request body or reliably enforce the `cn_exec_` prefix/filter as a security boundary. Therefore `packages/jetstream.EnsurePullConsumer` still validates the declared template to prevent application bugs, while the documented security boundary is the entire CloudNode Stream/KV, not one route. Tests must prove it cannot access Storage, Metrics, DLQ, other Streams/KV, or global JetStream management.

NATS ACL 生成器必须把“pull/ACK”展开为 exact consumer INFO/NEXT/ACK API subjects；不能发放 `$JS.API.CONSUMER.>` 或 `$JS.ACK.MOOX_STORAGE.>`。`storage-eventbus` 只获得两个 View Builder durable 的 INFO/NEXT/ACK subjects，`factor-eventbus` 只获得 `factor_calc` 的 INFO/NEXT/ACK subjects；两者都只 subscribe 自己请求使用的 `_INBOX.>` replies。

Checked-in broker, cluster, and health defaults stay loopback-only. Any client bind/advertise address outside loopback requires `auth.enabled`, `tls.enabled`, a `0600` users file, cert/key/CA, and a server certificate containing the advertised IP SAN. Production rendering sets `host: 0.0.0.0`, `client_advertise: <public-ip>:4222`, `internal_client.credential_file: $HOME/.config/moox/eventbus/internal-admin.yaml`, and the deployment CA path; there is no `allow_insecure` escape hatch. When broker auth is enabled, a missing/unsafe internal-client file fails startup before reconciliation. V1 rejects non-loopback cluster routes.

- [ ] **Step 3: Implement broker ownership**

`broker.Server` creates `natsserver.Options`, starts the server, waits for `ReadyForConnections`, exposes a local client URL, and shuts down only after clients and management workers have drained. It loads the deployment-rendered users file and requires `eventbus-internal-admin`, `hostagent-publisher`, `monitor-hostmetrics-consumer`, `storage-eventbus`, `cloudnode-eventbus`, and `factor-eventbus`. The embedded reconciliation client alone uses `eventbus-internal-admin`; business services never receive it. Each other identity gets the exact ACL defined by its owning migration task. V1 rejects non-loopback cluster routes and Stream replicas greater than one. Do not enable the NATS HTTP monitoring port; MooX exposes curated status through its own loopback health/metrics server.

The Host publisher is allowed only the exact Host Topic and its `_INBOX.>` PubAck replies. Monitor is allowed only fixed durable info/pull/ACK requests, its `_INBOX.>` replies, and `moox.dlq.message.rejected.v1`. Neither role receives `$JS.API.>` or `moox.>` wildcard management access. Start a real test broker and prove allow/deny cases for all six identities. Include Storage publication plus its two bind-only durables, Factor's one bind-only durable, the Host publisher, Monitor's durable/DLQ, and CloudNode's domain-scoped Stream/KV permissions. Prove that only internal admin can reconcile Streams/KV/concrete declared consumers outside the explicitly allowed CloudNode consumer lifecycle, and that cross-role Topic, durable, KV, and management access is denied.

- [ ] **Step 4: Add tRPC service configuration**

Use admin port `11960`, service name `trpc.moox.eventbus.EventBusMgr`, service port `11420`, and HTTP protocol consistent with the Monitor module. The NATS listener remains configured in `app.yaml` rather than as a tRPC service.

- [ ] **Step 5: Verify lifecycle and commit**

```bash
go test -count=1 ./modules/eventbus/internal/config ./modules/eventbus/internal/broker
git add go.work modules/eventbus
git commit -m "feat(eventbus): own embedded jetstream lifecycle"
```

---

### Task 5: Reconcile Streams, KV Buckets, And Topics

**Files:**
- Create: `modules/eventbus/internal/registry/registry.go`
- Create: `modules/eventbus/internal/registry/stream.go`
- Create: `modules/eventbus/internal/registry/kv.go`
- Create: `modules/eventbus/internal/registry/topic.go`
- Create: `modules/eventbus/internal/registry/consumer.go`
- Create: `modules/eventbus/internal/registry/registry_test.go`

- [ ] **Step 1: Write reconciliation tests**

Start a test broker and verify create, no-op restart, safe limit updates, rejection of subject removal with stored messages, rejection of retention-policy changes, KV TTL reconciliation, exact Topic/Topic-family-to-Stream validation, declared durable-consumer/template reconciliation, and preservation of consumer state. Assert all four concrete consumers exactly match the registry, including Factor's `deliver_policy=new`/`max_deliver=5` and Monitor's `deliver_policy=all`/`max_deliver=-1`. Assert the Host Topic is covered only by `MOOX_METRICS`, its payload type exactly matches `HostMetric`, and CloudNode route filters generated from different space/package/type tuples never overlap.

- [ ] **Step 2: Implement safe reconciliation**

Allow updates to `MaxAge`, `MaxBytes`, `MaxMsgs`, replicas, and description. Reject automatic changes to retention kind, storage kind, and subject removal when they can orphan existing messages. Return an error before readiness rather than silently accepting partial configuration.

For declared durable consumers, `stream`, durable name, filter subject, configured deliver policy (`all` except Factor's explicit `new`), `replay_policy=instant`, and `ack_policy=explicit` are immutable; a mismatch fails readiness and never deletes/recreates the consumer. `ack_wait`, `max_ack_pending`, and the configured `max_deliver` may update in place only when NATS confirms the existing delivery position is preserved. No implicit finite `max_deliver` is allowed for Monitor host metrics.

- [ ] **Step 3: Make readiness depend on reconciliation**

The service reports ready only after all configured Streams, KV buckets, and declared durable consumers exist and every enabled exact Topic/family is covered by exactly one Stream. Consumer templates are validated but create no consumer until an authorized business client supplies a concrete non-overlapping route.

- [ ] **Step 4: Run and commit**

```bash
go test -count=1 ./modules/eventbus/internal/registry
git add modules/eventbus/internal/registry
git commit -m "feat(eventbus): reconcile stream and topic registry"
```

---

### Task 6: Add Management RPC, Health, And Metrics

**Files:**
- Create: `modules/eventbus/internal/rpc/service.go`
- Create: `modules/eventbus/internal/rpc/service_test.go`
- Create: `modules/eventbus/internal/health/server.go`
- Create: `modules/eventbus/internal/health/server_test.go`
- Create: `modules/eventbus/internal/bootstrap/bootstrap.go`
- Create: `modules/eventbus/internal/bootstrap/bootstrap_test.go`
- Modify: `modules/eventbus/cmd/server/main.go`

- [ ] **Step 1: Write RPC and readiness tests**

Assert pagination, stable status conversion, consumer lag, missing resource errors, broker-not-ready behavior, and that no RPC can create/purge/delete a Stream or publish a message.

- [ ] **Step 2: Implement `EventBusMgr`**

Read broker and JetStream state through interfaces so unit tests do not require sockets. Sort Streams, Topics, and Consumers by stable names before pagination. Sanitize NATS errors into `common.RetInfo` without returning credentials or filesystem paths.

- [ ] **Step 3: Expose operational endpoints**

Implement:

```text
GET /healthz  process and broker liveness
GET /readyz   broker readiness plus successful resource reconciliation
GET /metrics  connection, stream bytes/messages, consumer pending/redelivery, publish advisories
```

Register bounded metric labels using Stream and Consumer names only.

- [ ] **Step 4: Implement ordered bootstrap and shutdown**

Startup order is config -> broker -> local JetStream client -> registry reconciliation -> management RPC -> health server. Shutdown order is health not-ready -> management stop -> client drain -> broker shutdown.

- [ ] **Step 5: Verify and commit**

```bash
go test -count=1 ./modules/eventbus/...
git add modules/eventbus
git commit -m "feat(eventbus): expose management and health surfaces"
```

---

### Task 7: Integrate Build, Release, Deploy, And SysDeploy

**Files:**
- Modify: `scripts/build.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `modules/admin/proto/sysdeploy_service.proto`
- Regenerate: `modules/admin/proto/admingen`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`
- Use (created by companion Host Agent Task 3): `modules/admin/cmd/cli/eventbus_credentials.go`
- Create: `skills/moox/scripts/eventbus-credentials.sh`
- Modify: `modules/README.md`
- Modify: `README.md`
- Create: `scripts/test-deploy-moox-eventbus.sh`

- [ ] **Step 1: Add failing deploy-package assertions**

The script must assert the presence of `bin/moox-eventbus`, `eventbus/config/app.yaml`, `data/eventbus/jetstream`, `logs/eventbus`, start-before-Storage ordering, stop-after-consumers ordering, and preservation under `--no-eventbus`. It must also scan release/stage/process arguments/logs for token and private-key leakage.

- [ ] **Step 2: Wire build and release**

Add `eventbus` to `scripts/build.sh`, release directories, binary copy, configuration copy, and the all-components build. Do not package JetStream runtime data, users files, Admin encryption keys, tokens, CA private keys, or server private keys.

- [ ] **Step 3: Provision EventBus security material before startup**

On a fresh install, invoke the companion credential CLI under `umask 077` to generate the Admin encryption key, a 10-year private CA, a 5-year server certificate with the configured public IP plus `127.0.0.1` and `::1` in IP SAN, and independent 256-bit tokens for `eventbus-internal-admin`, `hostagent-publisher`, `monitor-hostmetrics-consumer`, `storage-eventbus`, `cloudnode-eventbus`, and `factor-eventbus`. The CLI stores all token values and both certificate/private-key bundles encrypted in Admin `t_secrets` and exports runtime files with mode `0600`; this task never prints or regenerates values itself.

The six client outputs are exactly `$HOME/.config/moox/eventbus/internal-admin.yaml`, `$HOME/.config/moox/credentials/hostagent-publisher.yaml`, `$HOME/.config/moox/monitor/eventbus.yaml`, `$HOME/.config/moox/storage/eventbus.yaml`, `$HOME/.config/moox/cloudnode/eventbus.yaml`, and `$HOME/.config/moox/factor/eventbus.yaml`. The broker separately reads `$HOME/.config/moox/eventbus/users.yaml`; TLS files live under `$HOME/.config/moox/eventbus/tls/`. EventBus reads internal-admin, Monitor/Storage/CloudNode/Factor read only their own file, and the Host Agent file is only an input to the Skill's remote deploy. Formats and token field names are owned by Host plan section 9.1.

Normal deployments reuse all material. Existing Admin DB plus a missing encryption key fails closed. A changed public IP explicitly reissues only the server certificate under the same CA. Token or CA rotation requires a dedicated maintenance command and confirmation; it never happens during ordinary upgrade.

- [ ] **Step 4: Wire deployment lifecycle**

Add `--eventbus-public-ip`, `--no-eventbus`, stage path rewrites for `store_dir`, readiness waiting on `/readyz`, and this fixed order: Admin schema/encryption key ready -> reconcile `t_secrets` -> atomically render the six exact role files, broker users file, and TLS material -> render production broker config (`host: 0.0.0.0`, `client_advertise: <public-ip>:4222`, auth/TLS enabled and absolute runtime paths) -> start EventBus -> wait ready -> start Storage and then dependent consumers. Host companion Task 12 inserts its metadata apply/schema gate before Monitor host ingestion. Stop publishers/consumers before stopping EventBus. Reset and `--no-eventbus` preserve JetStream data and all credential files.

- [ ] **Step 5: Register the service in SysDeploy**

Add the deployment key `eventbus`, service name `trpc.moox.eventbus.EventBusMgr`, and Admin gateway route `/api/admin/eventbus/{method}` using the existing generated deployment model. The gateway allowlist contains only read-only management methods; there is no Publish route.

- [ ] **Step 6: Verify and commit**

```bash
./scripts/build.sh eventbus
./scripts/test-deploy-moox-eventbus.sh
go test -count=1 ./modules/admin/...
git add scripts skills/moox/scripts/eventbus-credentials.sh modules/admin modules/README.md README.md
git commit -m "feat(eventbus): package and deploy eventbus service"
```

---

### Task 8: Migrate Storage Messages To `MooxMessage`

**Files:**
- Use (declared by Task 4): `modules/eventbus/config/app.yaml`
- Modify: `modules/storage/proto/message.proto`
- Modify: `modules/storage/proto/store.proto`
- Regenerate: `modules/storage/proto/gen`
- Modify: `modules/storage/internal/service/eventbus/bus.go`
- Modify: `modules/storage/internal/service/eventbus/producer_bus.go`
- Modify: `modules/storage/internal/bootstrap/eventbus/factory.go`
- Modify: `modules/storage/internal/services/access/data.go`
- Modify: `modules/storage/internal/services/primary/client.go`
- Modify: `modules/storage/internal/services/primary/local.go`
- Modify: `modules/storage/internal/services/primary/remote.go`
- Modify: `modules/storage/internal/services/primary/service.go`
- Modify: `modules/storage/go.mod`
- Modify: `modules/storage/config/storage*.yaml`
- Test: focused Storage eventbus, access, primary, and proto tests

- [ ] **Step 1: Lock the Storage payload contracts**

Replace key-only `RowsChangedEvent` messages with the full-row `TimeSeriesRowsUpdated` and `RecordRowsUpdated` payloads defined by the existing write-journal materialization plan. Add descriptor tests for `message_id`, `written_at`, `space_id`, `dataset_id`, rows, and attributes.

- [ ] **Step 2: Extend the PrimaryStore internal write request**

Add `bytes outbox_message = 4` to `WritePrimaryRowsReq`. Access constructs one stable `MooxMessage` per routed PrimaryStore group, marshals it, and sends it with the rows. Local and remote clients preserve the same bytes across retries.

- [ ] **Step 3: Replace the private transport implementation**

Keep the domain `eventbus.Publisher/Subscriber` interfaces but implement them using `packages/jetstream`. Decode and validate `MooxMessage`, then unmarshal its domain payload. Remove Stream/consumer mutation from Storage startup because `moox-eventbus` owns `MOOX_STORAGE` and the two View Builder durables. Storage must use `BindPullConsumer`, never `EnsurePullConsumer`.

- [ ] **Step 4: Change configuration to the central endpoint**

Replace `embedded` settings with shared client configuration:

```yaml
eventbus:
  type: jetstream
  urls: ["tls://127.0.0.1:4222"]
  credential_file: "$HOME/.config/moox/storage/eventbus.yaml"
  tls_ca_file: "$HOME/.config/moox/eventbus/tls/ca.pem"
  stream_name: MOOX_STORAGE
  subject_prefix: moox.storage
  consumer_name: storage_view_builder
```

Retain `type: memory` only for explicitly single-process tests; production split-role configs use JetStream. `storage-access` is publisher-only and has no `consumer_name`; `storage-view-builder` derives exactly `storage_view_builder_time_series_rows_updated_v1` and `storage_view_builder_record_rows_updated_v1`; `storage-view-query` and `storage-view-index` do not initialize EventBus. V1 standard deployment rejects an independent `archive` role until its two exact durables are added to the EventBus registry.

- [ ] **Step 5: Verify message interoperability**

Publish through Storage Access, bind both predeclared View Builder durables, assert Topic/body/content type/producer identity, and confirm duplicate delivery does not duplicate View effects. Against a real role-authenticated broker, prove `storage-eventbus` can publish/bind/pull/ACK only those exact resources and cannot create/update/delete a consumer or bind `factor_calc`.

- [ ] **Step 6: Run and commit**

```bash
go test -count=1 ./modules/storage/internal/service/eventbus ./modules/storage/internal/service/eventbus ./modules/storage/internal/services/access ./modules/storage/internal/services/primary
git add modules/storage
git commit -m "refactor(storage): publish storage updates through moox messages"
```

---

### Task 9: Add The PrimaryStore Durable Outbox And Relay

**Files:**
- Modify: `modules/storage/internal/service/primary/device/store.go`
- Modify: `modules/storage/internal/service/primary/device/pebble/store.go`
- Create: `modules/storage/internal/service/primary/device/pebble/outbox.go`
- Create: `modules/storage/internal/service/primary/device/pebble/outbox_test.go`
- Create: `modules/storage/internal/services/primary/outbox_relay.go`
- Create: `modules/storage/internal/services/primary/outbox_relay_test.go`
- Modify: `modules/storage/internal/services/primary/service.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/config/storage*.yaml`
- Modify: `modules/storage/cmd/server/main.go`

- [ ] **Step 1: Write atomicity tests**

Inject failures before and during Pebble batch commit. Assert that rows and outbox entry are both absent or both present. Assert that a committed entry survives Store close/reopen with unchanged message bytes and ID.

- [ ] **Step 2: Extend the fact store interface**

```go
type OutboxEntry struct {
    Sequence  uint64
    MessageID string
    Topic     string
    Data      []byte
    CreatedAt time.Time
}

type FactStore interface {
    WriteRowsWithOutbox(ctx context.Context, rows []*pb.PrimaryStoreRow, entry *OutboxEntry) error
    ListOutbox(ctx context.Context, after uint64, maxItems int, maxBytes int) ([]*OutboxEntry, error)
    DeleteOutbox(ctx context.Context, sequences []uint64) error
    // Existing read and scan methods remain unchanged.
}
```

Use a reserved Pebble key prefix that cannot collide with fact rows. Allocate `sequence` in the same Pebble batch as data and outbox bytes.

- [ ] **Step 3: Make PrimaryStore success mean data plus outbox durability**

Validate the supplied `MooxMessage`, require its producer identity to match the local PrimaryStore node, and call `WriteRowsWithOutbox`. Return success without waiting for EventBus PubAck.

- [ ] **Step 4: Implement asynchronous batched relay**

The relay reads up to the first reached limit of `flush_batch_size`, `flush_max_bytes`, or `flush_interval`, calls `packages/jetstream.PublishBatch`, and deletes only entries with successful PubAck. Failures remain in Pebble and retry with bounded exponential backoff plus jitter. Independent messages retain independent ACK results.

- [ ] **Step 5: Add backlog limits and backpressure**

Configure `max_rows`, `max_bytes`, and `max_age`. Reject new writes before PrimaryStore mutation when the durable outbox exceeds its hard limit; never delete undelivered Storage events to make room. Export depth, bytes, oldest age, publish failures, and last PubAck time.

- [ ] **Step 6: Test outage and restart**

Stop EventBus, commit writes, restart the PrimaryStore process, start EventBus, and assert every message is eventually published. Simulate crash after PubAck but before delete and assert duplicate publication is harmless.

- [ ] **Step 7: Run and commit**

```bash
go test -count=1 ./modules/storage/internal/service/primary/device/pebble ./modules/storage/internal/services/primary ./modules/storage/internal/services/access
git add modules/storage
git commit -m "feat(storage): relay durable outbox to eventbus"
```

---

### Task 10: Migrate CloudNode To The Central EventBus

**Files:**
- Use (created by Task 2): `packages/jetstream/delivery.go`
- Use (created by Task 2): `packages/jetstream/kv.go`
- Modify: `modules/cloudnode/internal/jobqueue/queue.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_client.go`
- Modify: `modules/cloudnode/internal/jobqueue/jetstream_queue.go`
- Modify: `modules/cloudnode/internal/jobqueue/payload.go`
- Modify: `modules/cloudnode/internal/jobstate/kv_store.go`
- Modify: `modules/cloudnode/internal/jobstate/types.go`
- Modify: `modules/cloudnode/internal/rpc/job_item.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap.go`
- Modify: `modules/cloudnode/internal/bootstrap/bootstrap_test.go`
- Delete: `modules/cloudnode/internal/jobqueue/embedded.go`
- Modify: `modules/cloudnode/internal/config/config.go`
- Modify: `modules/cloudnode/config/app.yaml`
- Modify: `modules/cloudnode/go.mod`
- Test: CloudNode jobqueue and jobstate tests

- [ ] **Step 1: Write central-runtime compatibility tests**

Create `MOOX_CLOUDNODE_EXEC` and the active KV bucket through the EventBus registry fixture, then verify publish, poll, report/ACK, stale lease, redelivery, deduplication, and restart behavior through `packages/jetstream`. The restart case must persist a delivery token in job state, close/reconnect the shared client, then ACK/NAK/Term from `ReportJobItemStatus` without retaining an in-memory NATS message.

- [ ] **Step 2: Wrap job payloads in `MooxMessage`**

Preserve the current route naming contract:

```text
moox.cloudnode.exec.v1.jobitem.s.<space>.pkg.<package>.type.<job_type>
```

Set `MooxMessage.topic` to the exact routed Subject, Kind to `COMMAND`, Job ID as the stable message ID, the existing `JobItem` protobuf as payload, and `space_id` from the job. Preserve current attempt/lease checks above the transport. Add a Topic-family registry entry that binds the full pattern to the one `JobItem` schema. Replace persisted raw `AckSubject` with the shared package's bounded opaque `PersistentToken`; `ExecutionQueue` exposes token-based ACK/NAK/InProgress/Term without importing `nats.go`.

- [ ] **Step 3: Remove embedded-server ownership**

Replace `nats_url` plus `embedded` with `tls://127.0.0.1:4222`, `$HOME/.config/moox/cloudnode/eventbus.yaml`, and the deployment CA path. Remove `EnsureStreams`; CloudNode binds the precreated KV through `BindKV` and adapts `jobstate.KVStore` to the shared `KVStore` Create/Get/Update/Keys contract. It may ensure consumers only through the validated `cn_exec_` template and must not create/update the Stream or KV bucket.

The users-file ACL intentionally grants this role consumer lifecycle only inside `MOOX_CLOUDNODE_EXEC` and KV operations only for `MOOX_CLOUDNODE_JOB_ACTIVE`; prefix/filter validation is an application correctness check, not a broker security boundary. Real-broker tests allow valid CloudNode route consumers but deny every Storage/Metrics/DLQ subject, other Stream/KV, and global management API.

- [ ] **Step 4: Verify and commit**

```bash
go test -count=1 ./packages/jetstream ./modules/cloudnode/internal/jobqueue ./modules/cloudnode/internal/jobstate ./modules/cloudnode/internal/rpc ./modules/cloudnode/internal/bootstrap
git add modules/cloudnode
git commit -m "refactor(cloudnode): use central moox eventbus"
```

---

### Task 11: Migrate Factor And Remove Private NATS Implementations

**Files:**
- Use (declared by Task 4): `modules/eventbus/config/app.yaml`
- Modify: `modules/factor/internal/trigger/nats.go`
- Modify: `modules/factor/internal/app/control/config.go`
- Modify: `modules/factor/internal/app/control/bootstrap.go`
- Modify: `modules/factor/config/app.yaml`
- Modify: `modules/factor/go.mod`
- Delete: `modules/storage/internal/bootstrap/eventbus/embedded_nats.go`
- Delete: `modules/storage/internal/service/transport/nats/producer.go`
- Delete: private transport tests superseded by `packages/jetstream` tests
- Modify: `modules/storage/go.mod`

- [ ] **Step 1: Migrate Factor's Storage consumer**

Load `$HOME/.config/moox/factor/eventbus.yaml` and use `BindPullConsumer` for the predeclared `MOOX_STORAGE/factor_calc` durable filtered to `moox.storage.time_series.rows_updated.v1`; decode `MooxMessage`, validate the `trpc.moox.storage.TimeSeriesRowsUpdated` content type, and retain the current idempotent factor-run trigger behavior. Preserve `deliver_policy=new`, `ack_wait=60s`, `max_ack_pending=1000`, and `max_deliver=5`. Against a real role-authenticated broker, prove `factor-eventbus` can bind/pull/ACK only `factor_calc` and cannot publish Storage data, create/update/delete consumers, or bind either View Builder durable.

- [ ] **Step 2: Prove every production module uses the shared package**

Run:

```bash
rg 'nats-server/v2|github.com/nats-io/nats.go' modules packages \
  --glob '*.go' --glob 'go.mod'
```

Expected: `nats-server/v2` appears only in `modules/eventbus` and test files; `nats.go` appears only in `packages/jetstream`, `modules/eventbus`, and test fixtures.

- [ ] **Step 3: Delete superseded implementations and tidy modules**

Run `go mod tidy` in EventBus, Storage, CloudNode, Factor, `packages/messagepb`, and `packages/jetstream`. Confirm no production module can enable an embedded broker through YAML.

- [ ] **Step 4: Run and commit**

```bash
go test -count=1 ./modules/factor/... ./modules/storage/... ./modules/cloudnode/... ./packages/jetstream
git add modules packages
git commit -m "refactor(eventbus): remove module-private nats runtimes"
```

---

### Task 12: End-To-End Failure, Load, And Deployment Verification

**Files:**
- Create: `modules/eventbus/test/eventbus_e2e_test.go`
- Create: `docs/运维/MooX-EventBus运维.md`
- Modify: `modules/eventbus/README.md`
- Modify: `README.md`

- [ ] **Step 1: Add end-to-end integration coverage**

Start standalone EventBus with a temporary persistent StoreDir, private CA, IP SAN, and all six production-equivalent identities. Publish Storage/Host metrics/CloudNode messages, bind independent durable consumers, restart EventBus, and verify Stream contents, consumer positions, KV CAS state, persistent-token ACK after reconnect, PubAck, redelivery, typed `RejectedMessage` DLQ publication, and all ACL deny cases. CloudNode allow tests use only its Stream/KV domain; deny tests prove it cannot cross into Storage, Metrics, DLQ, or global management.

Attempt a non-loopback cluster configuration and a Stream replica count greater than one; both must fail validation in V1. Standalone restart is the only production availability test in this plan.

- [ ] **Step 2: Verify Host Agent best-effort versus durable consumption**

Stop EventBus for three Host Agent sampling periods. The Host-Agent-style publisher records three failed attempts, retains no disk or memory replay queue, and publishes only a new sample after recovery. Then keep EventBus running, stop the Monitor-style consumer, publish samples with PubAck, restart Monitor, and prove the fixed durable delivers all accepted samples. This test makes the pre-PubAck/post-PubAck reliability boundary explicit.

- [ ] **Step 3: Add bounded load checks**

Publish at least 100,000 independent 1 KiB messages through `PublishBatch`, maintain multiple durable consumers, and record throughput, TLS overhead, p95 PubAck latency, process RSS, disk growth, pending counts, and recovery time after a 30-second broker outage. The test uses the production-style private CA and role ACLs, enforces a temporary-directory byte cap, and never scans production data.

- [ ] **Step 4: Verify Storage outbox recovery**

Write while EventBus is unavailable, restart Storage, restore EventBus, and verify outbox depth returns to zero without missing logical IDs. Repeat a crash after PubAck and prove consumer idempotency.

- [ ] **Step 5: Verify packaged deployment**

Run:

```bash
./scripts/build.sh all
./scripts/release.sh
./scripts/test-deploy-moox-eventbus.sh
go test -count=1 ./modules/eventbus/... ./packages/messagepb ./packages/jetstream
```

Expected: all commands pass; the release contains no runtime JetStream data, token, users file, Admin encryption key, CA private key, or server private key; non-loopback plaintext/insecure startup fails; EventBus readiness succeeds before dependent services start.

- [ ] **Step 6: Document operations and commit**

Document backup/restore of `data/eventbus/jetstream`, Admin root key and EventBus secret rows, private CA/server certificate reissue, public-IP change, capacity alerts, consumer-lag diagnosis, maintenance-window token rotation, ACL diagnosis, and the rule that callbacks are asynchronous durable consumers rather than part of the publish path.

```bash
git add modules/eventbus docs/运维 README.md
git commit -m "docs(eventbus): add verification and operations guide"
```

---

## Final Acceptance Criteria

- All production MooX application messages use `MooxMessage` and a versioned Topic.
- `topic` exactly equals the NATS Subject; `Nats-Msg-Id` equals `message_id`.
- No production module except `moox-eventbus` embeds `nats-server/v2`.
- No production module outside `packages/jetstream`/`modules/eventbus` imports `nats.go`; CloudNode KV CAS and reconnect-safe ACK use the shared API.
- Storage writes wait only for data plus local outbox durability, not EventBus PubAck.
- Storage Relay batches independent async publications and deletes only PubAcked entries.
- EventBus owns all Stream/KV retention configuration and refuses unsafe automatic changes.
- The management plane is read-only and cannot publish messages.
- Host metrics use only `moox.metrics.host.reported.v1` with `trpc.moox.hostagent.HostMetric`; no HTTP/tRPC publish path exists.
- DLQ uses only `moox.dlq.message.rejected.v1` with `trpc.moox.message.RejectedMessage` and never copies rejected raw payloads.
- Every non-loopback NATS connection verifies the deployment private CA and advertised IP SAN; plaintext and insecure verification are impossible by configuration.
- Host Agents share only the scoped publisher token, and Monitor uses a distinct scoped consumer token; real allow/deny ACL tests pass.
- Storage/Factor are exact bind-only roles; CloudNode's explicitly documented security boundary is its entire Stream/KV and all cross-domain requests are denied.
- Deployment creates/reuses all six role tokens and both TLS bundles in Admin `t_secrets`; release artifacts contain no token or private key.
- All six client runtime files, broker users file, and TLS paths match the fixed deployment-user layout and reader ownership.
- Host Agent has no outbox or replay queue: failed pre-PubAck samples are dropped, while PubAcked samples remain durably consumable by Monitor.
- JetStream restart preserves messages, durable positions, and KV state.
- Consumers tolerate duplicate and redelivered messages.
- Build, release, deploy, unit, integration, outage, and bounded load checks pass.
