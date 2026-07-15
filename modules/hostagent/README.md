# moox-host-agent

`moox-host-agent` is an independent Linux amd64/arm64 user service. It reads CPU, memory, filesystem, disk I/O, and network counters from Linux kernel interfaces and publishes one best-effort `MooxMessage` every 15 seconds to `moox.metrics.host.reported.v1` through the shared JetStream client.

The agent does not persist samples or contain an embedded broker. Its only persistent state is the 0600 identity file. Configure the EventBus credential file outside the release tree and run it with the user systemd unit supplied by the deployment skill.

Set `host_name` in `config/app.yaml` to the corresponding SSH host name when the operating-system hostname differs from the name shown in the Host Workbench.
