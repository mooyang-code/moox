# Collector SCF Invoke Failover Design

## Goal

When the Collector control plane cannot complete `InvokeFunction` for a
planned market-fetch batch, immediately retry the same event on one other
active market-fetcher node. This reduces freshness gaps caused by a single
SCF node or gateway request timing out without creating a fan-out storm.

## Behavior

- First invoke the planned node.
- On any invocation error, select the next active node from the same snapshot
  of deployed market-fetcher nodes and invoke the same batch event once.
- Never invoke the same node twice for one dispatch attempt. A batch therefore
  has at most two immediate invoke requests: the original node and one
  failover node.
- Apply the same behavior to normal realtime/snapshot/catch-up dispatches and
  retry-queue dispatches.
- If failover succeeds, persist the selected node's region, node ID, function
  name, request ID, and dispatch deadline before marking the batch dispatched.
- If both attempts fail, leave the batch in its existing planned state. The
  current deadline recovery and per-item retry queue remain the final retry
  path.

## Idempotency and trade-offs

An HTTP timeout can mean the first request was accepted by CloudNode but its
response was lost. Failover may therefore execute the same batch twice. The
event uses the same batch ID and existing Storage/completion deduplication, so
duplicate writes are safe for the current collector contract. The bounded
single-failover policy prevents a gateway outage from multiplying calls across
the entire SCF fleet.

## Observability and tests

Failover logs include batch ID, source node, target node, and the original
error. Tests cover first-node failure with alternate success, both-node
failure, retry dispatch, persisted node identity, and a single-node pool.
