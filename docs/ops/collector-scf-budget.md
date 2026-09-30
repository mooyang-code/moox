# Collector SCF Execution Budget

StockCN Timer functions use a 60 second timeout. Each Timer request is bound to
one source; its item count is capped at the smaller of `measured_safe_group_size`
and the runtime limit of 40. Provider time is
`ceil(items / max_inflight_requests) * 4 attempts * request_timeout_ms`.

StockCN Invoke functions default to a minimum timeout of 90 seconds. The Invoke
request budget uses its actual active provider chain (currently Sina, Tencent,
TDX and EastMoney), not the single-source Timer formula:
`ceil(realtime_batch_size / max_inflight_requests) * 4 providers * 4 attempts * request_timeout_ms`.
The route synchronization test detects changes to this provider count.

Both paths reserve one shared 5 second Storage write window across all target
Datasets, 13 seconds for Completion, 3 seconds for CLS, 750 milliseconds for
metrics/response work and 500 milliseconds for the final response. Completion
covers two attempts, each with a 3 second cold connection and 3 second publish
ACK, plus a 300 millisecond backoff. Timer additionally reserves 3 seconds for
Claim. Configuration validation rejects a budget equal to or exceeding the
applicable timeout before publication. Best-effort instrument-name reads have
an independent 250 millisecond timeout included in both conservative budgets;
they do not start the shared Storage write window.
