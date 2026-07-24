# moox-host-agent

`moox-host-agent` is an independent Linux amd64/arm64 user service. It reads CPU, memory, filesystem, disk I/O, and network counters and publishes one best-effort governed `EventMessage` every 15 seconds to the `metrics.host.reported` event through the shared JetStream client.

Sampling is owned by `trpc.moox.hostagent.sample.timer`, uses a fixed deployment-time 15-second schedule, and runs once immediately at service startup. Scheduled and manual `RunOnce` calls share the Agent's local non-reentrant guard, so only one collection can run at a time. The `startAtOnce=1` handler is synchronous: an initial collection or publishing failure prevents successful Timer service startup and lets the process supervisor retry. Runtime frequency changes are intentionally unsupported.

Each sample has an explicit 30-second timeout. The handler clones invocation metadata so an upstream request deadline cannot truncate the scheduled work, while server shutdown still cancels the active sampling context. `DefaultScheduler` is process-local and does not elect a cluster-wide owner; this is intentional because every HostAgent must sample its own host.

The agent does not persist samples or contain an embedded broker. Its only persistent state is the 0600 identity file. Configure the EventBus credential file outside the release tree and run it with the user systemd unit supplied by the deployment skill.

Set `host_name` in `config/app.yaml` to the corresponding SSH host name when the operating-system hostname differs from the name shown in the Host Workbench.
