# Observability Operations

This runbook describes the simple monitoring boundary used by the MooX
personal-quant deployment. It separates facts proven by local tests from checks
that require a real deployment.

## Naming

EventBus subjects and Prometheus metric names are different namespaces:

- Observability events use the single `moox.observability.>` prefix and the
  `MOOX_OBSERVABILITY` stream.
- Monitor binds the single durable
  `monitor_observability_ingest_v1` to that prefix.
- Metrics use `moox_<module>_<metric>` names. Dataset metrics have only
  `space_id`, `dataset_id`, and `freq` identity labels. A service process is
  identified by `service_name`, `instance_id`, and `node_id` in its report
  envelope, not by adding those values to every custom metric.

Do not construct EventBus subjects from metric names or treat a protobuf event
name as a Prometheus metric name.

## Dataset Discovery

The expected realtime dataset registry is derived from business configuration:

- Collector includes every enabled scheduled K-line rule and every configured
  frequency. The `live` execution hint does not change monitoring inventory;
  disabled, symbol, and record datasets are excluded.
- Factor includes a target dataset and frequency only while both its binding
  and referenced factor are enabled.
- Duplicate `(space_id, dataset_id, freq)` tuples are collapsed.
- Startup fails if the first complete inventory cannot be built. Runtime
  refresh failures retain the previous complete inventory and are retried by
  the existing metrics timer.

Storage separately maintains a bounded process-local union of time-series
tuples for which that PrimaryStore process has actually attempted a commit.
This is commit evidence, not a second enabled-dataset inventory.

## Timestamp Semantics

The four dataset timestamps answer different questions:

| Metric | Meaning |
| --- | --- |
| `last_run` | The latest terminal attempt, including success, empty, or error. |
| `last_success` | The latest successful non-error run. |
| `input_watermark` | The newest business timestamp consumed by a committed calculation. |
| `output_watermark` | The newest business timestamp durably written by the reporting stage. |

Watermarks are monotonic. A replay of an older interval can increment run
counters but cannot move a watermark backwards. Empty and failed runs do not
advance output watermarks.

Collector advances its output watermark from the largest K-line `data_time`
acknowledged by Storage. PrimaryStore advances only after the target DataNode
accepts all rows in that dataset group. Factor advances input and output
watermarks only after `WriteFactorPatch` succeeds.

Storage View additionally exports
`moox_storage_view_output_watermark_timestamp_seconds{space_id,view_id,freq}`.
Monitor treats each View/frequency tuple as a separate `storage_view` freshness
row, so a lagging Factor View raises its own watermark-lag check without
masking or being masked by the K-line View or Primary dataset.

## Failure Boundaries

| Failure | Detection and recovery |
| --- | --- |
| Monitor process exits | systemd or cron restarts it. This does not detect a dead Monitor host. |
| Monitor host is unreachable | The designated SCF Sentinel fails `monitor_ready` and sends through the configured notification channel. |
| HostAgent exits or its host disconnects | Monitor marks that stable four-character `agent_id` unreachable after 90 seconds. Presence state is in SQLite and survives Monitor restart; pre-migration UUIDs remain queryable as aliases. |
| HostAgent identity upgrade/rollback | The runtime uses the compact ID, while the identity file keeps the legacy UUID and `compact_agent_id` during the compatibility window. Roll back HostAgent and Monitor as a versioned pair; do not mix a pre-compact Monitor with a database already migrated to compact alert identities. |
| One of two same-named service instances exits | Report envelope identity keeps `instance_id` and `node_id` distinct; only the missing instance is stale. |
| SCF heartbeat stops | CloudNode changes that function to timeout and excludes it from scheduling. |
| EventBus is unavailable | Monitor readiness becomes false. SCF health publication fails and the Sentinel uses the configured notification channel. |
| A business check fails while the central path is healthy | The result goes through EventBus and Monitor; SCF does not bypass Monitor. |

When a HostAgent is registered and the WeCom webhook is configured, Monitor
creates a per-agent `filesystem_usage` alert in `moox_system`. It fires after
three consecutive 15-second samples at 85% filesystem usage and resolves at
80%. This is intentionally earlier and faster than the CPU/memory defaults so
Storage/View operators still have time to remove stale release artifacts
before the host reaches `ENOSPC`. A machine without a running HostAgent has no
filesystem sample and therefore cannot produce this alert; deploy and verify
the agent's EventBus credentials before treating the absence of an alert as a
healthy disk.

Only one Collector SCF node should enable the Sentinel. Its 30-second timer runs
inside the verified resident SCF process. If the platform freezes the process
before the final resident window completes, one last external alert may be
lost; V1 intentionally adds no lease or second scheduler.

The control profile binds only the Gateway and Monitor readiness listeners to
`0.0.0.0:11012` and `0.0.0.0:11409`. The setup command opens those two TCP
ports because Tencent SCF does not have a stable egress CIDR in this deployment.
They expose diagnostics only, reject unsigned requests through the dedicated
health HMAC, and are not routed by Caddy. Business and management listeners
remain loopback-only. Rotate `health-auth.env` if that credential is exposed.

Direct SCF notification is reserved for failure of the central Monitor or
EventBus path. Normal service, dataset, balance, and market-canary alerts remain
owned by Monitor so cooldown and recovery state have one owner.

## Local Verification

Run the full local suite:

```bash
bash scripts/verify-observability-e2e.sh
```

It runs race-enabled tests for the reporting, event, notification, HostAgent,
Monitor, Collector, CloudNode, Storage, Factor, and Trade modules, followed by
HostAgent release/deploy contracts and the Monitor coverage contract.

The local fault tests use embedded NATS, temporary SQLite databases, in-memory
Storage adapters, and HTTP test servers. They prove:

- unified Metrics, Host, and Health routing through one durable;
- EventBus de-duplication, transient NAK/redelivery, permanent TERM, and durable
  resume after Monitor restart;
- 90-second host silence after reopening the same SQLite database;
- isolation of two agents with the same hostname;
- Collector success, empty, error, stale delivery, and monotonic watermark
  behavior through MetricSnapshot creation;
- a read-only BTC-USDT-shaped canary that judges `/readyz` and a business query,
  while deliberately ignoring a service-root `404`.

## Deployment Verification

### EventBus and SCF Governance

For a remote deployment that enables Collector SCF, `scripts/deploy-moox.sh`
requires `MOOX_EVENTBUS_PUBLIC_IP` and TLS. It rejects loopback-only topology
before stopping the existing release. The generated `config/runtime.env`
persists the EventBus listener host, port, and TLS mode so a restart cannot
silently fall back to `127.0.0.1`. After startup the deployment acceptance
check verifies that the file matches the process and that EventBus is listening
on the public interface.

The config-driven `moox-cli collector function publish` path rewrites the SCF
EventBus endpoint from `custom.toml` while retaining the role credential and CA;
it refuses a loopback address, missing CA, invalid port, or disabled TLS. Run
`moox-cli setup e2e-eventbus --file custom.toml` after a production deployment
to prove external TLS connectivity, JetStream publish/fetch/ACK, and worker
ACLs. `MOOX_EVENTBUS_ALLOW_LOOPBACK_REMOTE=1` is an explicit private/VPN escape
hatch and should not be used for Tencent SCF fleets.

Local tests do not prove a remote deployment. Record these facts from the
target environment:

1. `MOOX_OBSERVABILITY` stream and
   `monitor_observability_ingest_v1` consumer information.
2. `/readyz` for every service instance, including `instance_id` and `node_id`.
3. Every HostAgent `last_seen_at`.
4. SCF `online -> timeout -> online` after stopping and restoring heartbeat.
5. Collector, Storage, and Factor watermarks for the real
   `(crypto, market_kline, 1m)` or selected deployed tuple.
6. A read-only query for the latest two closed BTC-USDT 1m bars, including the
   exact space, dataset, subject, frequency, query time, and returned watermark.
7. Triggered and resolved enterprise notification messages.

A service-root `404` is not a deployment verdict. Use `/readyz` plus a real,
read-only business query. Never write fabricated remote results into the
verification record when only the local suite was run.
