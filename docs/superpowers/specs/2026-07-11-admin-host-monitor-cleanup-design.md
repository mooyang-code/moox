# Admin Legacy Host Monitor Cleanup Design

> **状态：已实施。** Admin 旧主机监控已删除，当前系统架构见 [主机监控架构设计](../../主机监控架构设计.md)。

## Goal

Remove the retired Admin host-monitoring implementation and improve the existing `/ops/resource-monitor` page for a fleet of at most ten hosts.

The only supported host-monitoring path remains:

`moox-host-agent -> EventBus -> moox-monitor -> MooX Storage -> Monitor API -> Web`

## Scope

### Remove From Admin

Delete the complete legacy `modules/admin/internal/service/monitor` package, including:

- Node Exporter scraping, parsing, and rate calculation.
- Host-monitor history DAO and models.
- Current and historical metrics services and RPC adapters.
- Collection and cleanup timer handlers.

Remove the corresponding Admin bootstrap wiring:

- `Services.Monitor`.
- Monitor service construction and global initialization.
- `trpc.moox.ops.Monitor` registration.

Remove the legacy Admin protocol surface:

- Host metrics and history messages from `ops_service.proto`.
- The `Monitor` service definition.
- Regenerated Admin protobuf and tRPC files.
- Obsolete Admin tRPC service configuration, if it has a dedicated service entry.

Remove configuration that exists only for Node Exporter collection:

- `monitor.node_exporter_port`.
- `monitor.collect_timeout`.
- `monitor.concurrent_limit`.
- `NODE_EXPORTER_PORT`.
- `MONITOR_COLLECT_TIMEOUT`.
- `MONITOR_CONCURRENT_LIMIT`.

Remove `t_host_monitor_history` and its indexes from the Admin schema used for new databases. Do not automatically drop an existing table during Admin startup. Existing installations may delete the old table after backup; the application will no longer read or write it.

Remove obsolete Node Exporter instructions and statements that describe Admin as the host metrics collector. Keep the current Host Agent, EventBus, Monitor, and Storage deployment guidance.

### Keep

- SSH host management and terminal features.
- The `/ops/resource-monitor` route and menu item.
- `web/src/api/modules/host-monitor.ts`, which calls the Monitor control API.
- Monitor host APIs, Storage datasets, retention, and alert evaluation.

## Frontend Design

The page keeps a host-card wall because the expected fleet contains at most ten hosts.

### Summary Bar

Show these values in one compact, unframed header row:

- Online hosts and total hosts.
- Hosts needing attention.
- Storage history status.
- Latest successful refresh time.
- Manual refresh and auto-refresh controls.

Do not keep four oversized statistic cards. The page should prioritize the host cards rather than aggregate decoration.

### Host Cards

Use a responsive grid with one column on narrow screens, two on medium screens, and three on wide screens. Each card shows:

- Host name, address, online state, and last-seen age.
- CPU, memory, and maximum filesystem usage.
- Aggregate network receive and transmit rates.
- Explicit unavailable values as `--`.
- A restrained warning state when any available percentage reaches 80 percent.

Clicking a card selects the host and updates the details below it. Selection uses the stable `agent_id`, not the hostname or address.

### Selected Host Details

The detail area contains two un-nested sections:

- Resource trend for 1 hour, 24 hours, or 3 days.
- Filesystem, disk, and network interface tables.

The trend chart excludes unavailable samples instead of plotting zero. Auto-refresh updates current cards every five seconds but does not reload history. History reloads only when the selected host, time range, or manual refresh changes.

### States

- Loading: preserve card and chart dimensions and show local spinners.
- Empty fleet: show one compact empty state.
- Host offline: keep the card visible, show last seen, and render unavailable metrics as `--`.
- Storage unavailable: keep current metrics visible and show a history-only warning.
- Historical data gap: show a non-blocking warning above the chart.
- API failure: preserve the last successful data and show a refresh error.

## Data Mapping

The frontend continues to consume `ListHostAgents` and `QueryHostMetricHistory` through the Admin control gateway.

Improve mapping rules:

- Use the maximum available filesystem percentage for the card summary.
- Sum available network rates across interfaces.
- Keep filesystem, disk, and network rows for the selected-host tables.
- Preserve availability flags through chart conversion.
- Use `last_seen_at` for freshness and display age.

No new backend API is required unless implementation reveals that the current Monitor response omits a field needed by the device tables.

## Testing

Backend checks:

- Admin compiles and tests without the monitor package.
- Generated Admin protobuf and tRPC files contain no Monitor service.
- Admin configuration tests no longer mention Node Exporter settings.
- New Admin schema tests confirm the old history table is absent.
- Repository-wide searches find no runtime import or registration of the deleted package.

Frontend checks:

- Unit-test API mapping helpers for maximum filesystem usage, aggregated network rates, stable agent selection, and unavailable values.
- Build the production frontend.
- Verify desktop and mobile layouts with browser screenshots.
- Verify online, offline, empty, Storage unavailable, and data-gap states.

## Non-Goals

- Do not add another host-monitoring service or storage path.
- Do not restore Admin-side Node Exporter polling.
- Do not migrate old Admin history rows into Storage because they lack a reliable Host Agent identity.
- Do not change Host Agent collection semantics, EventBus subjects, Storage retention, or host alert rules.
