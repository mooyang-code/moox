# MooX Host Workbench Monitoring Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a full host-monitoring Tab to Host Workbench, provide card and master-detail layouts, and deploy verified Host Agents to both registered production hosts.

**Architecture:** Keep the existing `moox-host-agent -> EventBus -> moox-monitor -> Storage -> Monitor API` pipeline. The web page independently loads SSH inventory and Monitor snapshots, merges them with strict identity rules, and passes one shared state model to two presentation components. Host Agent deployment preserves credentials and identity while replacing the current binary with a freshly built version.

**Tech Stack:** Vue 3, TypeScript, Arco Design Vue, VChart, Vitest, Vite, Go, tRPC, systemd user services, MooX EventBus and Monitor.

---

## File Map

Create:

- `web/src/views/ops/host-workbench/host-monitor-mapping.ts`: strict SSH/Agent merge, card summaries, attention state, and view-mode persistence helpers.
- `web/src/views/ops/host-workbench/host-monitor-mapping.test.ts`: mapping and summary unit tests.
- `web/src/views/ops/host-workbench/host-monitor.vue`: API orchestration, shared selection, refresh lifecycle, layout switch, and page states.
- `web/src/views/ops/host-workbench/host-monitor-card-grid.vue`: default responsive card layout.
- `web/src/views/ops/host-workbench/host-monitor-master-detail.vue`: left-host/right-detail layout.
- `web/src/views/ops/host-workbench/host-monitor-detail.vue`: VChart trend and filesystem, disk, and network tables.

Modify:

- `web/src/views/ops/host-workbench/index.vue`: add the monitor Tab, normalize three Tab values, and remove the global refresh control.
- `web/src/router/route.ts`: redirect the legacy resource-monitor route to `tab=monitor`.
- `web/src/api/modules/host-monitor.ts`: export raw Host Agent types only where required by the new pure mapping boundary.
- `web/scripts/check-host-monitor-contract.mjs`: enforce the monitor Tab, layouts, route, API calls, and scroll contracts.
- `web-host/internal/statik/statik.go`: regenerate embedded production assets.

Deploy without storing secrets in Git:

- `bin/moox-host-agent`: fresh Linux amd64 binary.
- `~/.local/lib/moox/hostagent/releases/<version>/`: versioned Agent release on each host.
- Existing `~/.config/moox/hostagent/eventbus.yaml`, identity file, and user systemd units remain authoritative.

## Task 1: Monitoring Domain Model and Tests

**Files:**

- Create: `web/src/views/ops/host-workbench/host-monitor-mapping.ts`
- Create: `web/src/views/ops/host-workbench/host-monitor-mapping.test.ts`
- Modify: `web/src/api/modules/host-monitor.ts`

- [ ] **Step 1: Write failing identity and summary tests**

Cover exact unique-name match, exact address match, SSH-only, Agent-only, duplicate-name ambiguity, unavailable metrics, 80% attention threshold, maximum filesystem usage, and network aggregation. Use this public shape:

```ts
export interface HostMonitorRow {
  key: string;
  ssh?: SSHHost;
  monitor?: HostMetrics;
  match: 'address' | 'unique_name' | 'unmatched';
  displayName: string;
  displayAddress: string;
  state: 'online' | 'offline' | 'unmonitored';
  cpuUsage: number | null;
  memoryUsage: number | null;
  filesystemUsage: number | null;
  networkRate: { rx: number; tx: number } | null;
  attention: boolean;
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
CI=true pnpm -C web vitest run --config vitest.config.ts src/views/ops/host-workbench/host-monitor-mapping.test.ts
```

Expected: FAIL because `host-monitor-mapping.ts` does not exist.

- [ ] **Step 3: Implement the minimal pure mapping**

Export:

```ts
export function buildHostMonitorRows(monitors: HostMetrics[], sshHosts: SSHHost[]): HostMonitorRow[]
export function normalizeMonitorViewMode(value: string | null): 'cards' | 'master'
```

Match unused SSH hosts by exact normalized address first, then by a name that appears exactly once on both sides. Never use substring, fuzzy, or DNS matching. Derive availability with the existing host-monitor helpers.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run the Step 2 command. Expected: PASS for all identity, state, and summary cases.

- [ ] **Step 5: Commit the domain boundary**

```bash
git add web/src/api/modules/host-monitor.ts web/src/views/ops/host-workbench/host-monitor-mapping.ts web/src/views/ops/host-workbench/host-monitor-mapping.test.ts
git commit -m "feat(ops): model host monitoring inventory"
```

## Task 2: Three-Tab Workbench and Shared Monitoring State

**Files:**

- Create: `web/src/views/ops/host-workbench/host-monitor.vue`
- Modify: `web/src/views/ops/host-workbench/index.vue`
- Modify: `web/src/router/route.ts`
- Modify: `web/scripts/check-host-monitor-contract.mjs`

- [ ] **Step 1: Extend the contract check before production code**

Require these source contracts:

```text
key="monitor" title="主机监控"
/ops/resource-monitor -> /ops/hosts?tab=monitor
getCurrentMetrics()
listSSHHosts({ limit: 500 })
15_000 auto-refresh interval
cards/master view mode
```

- [ ] **Step 2: Run the contract check and verify RED**

```bash
CI=true pnpm -C web check:host-monitor
```

Expected: FAIL because Host Workbench has no monitor Tab and the redirect still targets `hosts`.

- [ ] **Step 3: Implement typed three-Tab routing**

In `index.vue`, normalize query values with:

```ts
type HostTab = 'hosts' | 'monitor' | 'sessions';
const normalizeTab = (value: unknown): HostTab => value === 'monitor' || value === 'sessions' ? value : 'hosts';
```

Render Tab order `hosts`, `monitor`, `sessions`. Remove the workbench-level “刷新监控” button and summary strip; mount `<HostMonitor />` only in the monitor pane. Preserve terminal and file-manager actions in the host pane.

- [ ] **Step 4: Implement shared monitor state**

`host-monitor.vue` must:

```ts
const AUTO_REFRESH_MS = 15_000;
const viewMode = ref<'cards' | 'master'>(normalizeMonitorViewMode(localStorage.getItem(VIEW_MODE_KEY)));
const selectedKey = ref('');
```

Load `getCurrentMetrics()` and `listSSHHosts({ limit: 500 })` with `Promise.allSettled`, retain last successful values per source, derive `HostMonitorRow[]`, and select the first row only when the previous selection no longer exists. Auto-refresh updates current data only.

- [ ] **Step 5: Verify contract GREEN and commit**

Run `CI=true pnpm -C web check:host-monitor`; expected PASS.

```bash
git add web/src/views/ops/host-workbench/index.vue web/src/views/ops/host-workbench/host-monitor.vue web/src/router/route.ts web/scripts/check-host-monitor-contract.mjs
git commit -m "feat(ops): add host monitoring tab"
```

## Task 3: Card, Master-Detail, and Resource Detail Views

**Files:**

- Create: `web/src/views/ops/host-workbench/host-monitor-card-grid.vue`
- Create: `web/src/views/ops/host-workbench/host-monitor-master-detail.vue`
- Create: `web/src/views/ops/host-workbench/host-monitor-detail.vue`
- Modify: `web/src/views/ops/host-workbench/host-monitor.vue`

- [ ] **Step 1: Add layout assertions before implementation**

Extend `check-host-monitor-contract.mjs` to require the three focused components, a segmented view control, local overflow containers, `min-height: 0`, 1h/24h/3d ranges, and explicit filesystem, disk I/O, and network headings.

- [ ] **Step 2: Run the contract and verify RED**

Run `CI=true pnpm -C web check:host-monitor`. Expected: FAIL because the layout components do not exist.

- [ ] **Step 3: Implement default card view**

Use an Arco radio/button group with icon labels for `cards` and `master`. Cards show name, address, state text, last report, CPU, memory, maximum filesystem usage, and aggregate network rates. Values unavailable for offline/unmonitored rows render `--`. The grid uses 3/2/1 responsive columns.

- [ ] **Step 4: Implement master-detail view**

Use a stable two-column grid with a bounded left host list and a `minmax(0, 1fr)` detail track. On narrow screens, collapse to one column. Both views emit the same stable row key and use the parent selection.

- [ ] **Step 5: Implement selected-host details**

Move the working VChart/history and device-table logic from the legacy resource monitor into `host-monitor-detail.vue`. Accept the selected `HostMonitorRow`, call `getHistoryMetrics(monitor.host_id, duration)`, release VChart on data changes/unmount, and render local warnings for Storage unavailable or gaps. Use fixed-size chart and scrollable tables so dynamic content cannot resize the page.

- [ ] **Step 6: Verify contracts, typecheck, and commit**

```bash
CI=true pnpm -C web check:host-monitor
CI=true pnpm -C web exec vue-tsc --noEmit
git add web/src/views/ops/host-workbench
git commit -m "feat(ops): rebuild host monitoring views"
```

Expected: contract and typecheck exit 0.

## Task 4: Full Frontend Verification and Web Deployment

**Files:**

- Modify: `web-host/internal/statik/statik.go`

- [ ] **Step 1: Run fresh frontend tests**

```bash
CI=true pnpm -C web vitest run --config vitest.config.ts
CI=true pnpm -C web check:host-monitor
CI=true pnpm -C web exec vue-tsc --noEmit
CI=true pnpm -C web run build:prod
```

Expected: all commands exit 0. Existing Sass, Browserslist, and chunk-size warnings may remain, but no test, type, or build error may remain.

- [ ] **Step 2: Regenerate embedded assets and test web-host**

```bash
cd web-host
statik -src=../web/dist -dest=./internal
gofmt -w internal/statik/statik.go
go test -count=1 ./...
TARGET_GOOS=linux TARGET_GOARCH=amd64 ../scripts/build/build.sh web-host
```

Expected: tests pass and `bin/moox-web-host` is a Linux amd64 executable.

- [ ] **Step 3: Commit embedded assets and deploy**

```bash
git add web-host/internal/statik/statik.go
git commit -m "build(web): embed host monitoring workbench"
scp bin/moox-web-host ubuntu@106.53.107.122:/tmp/moox-web-host.new
ssh ubuntu@106.53.107.122 'cd /home/ubuntu/moox/prod && MOOX_WITH_WEB_HOST=1 ./stop.sh web-host && install -m 0755 /tmp/moox-web-host.new bin/moox-web-host && MOOX_WITH_WEB_HOST=1 ./start.sh web-host'
```

Expected: `web-host` restarts with a new PID.

## Task 5: Rebuild and Deploy Host Agent to Both Hosts

**Files:** no tracked configuration changes unless a test exposes a defect.

- [ ] **Step 1: Run Host Agent tests and build Linux binary**

```bash
go test -count=1 ./modules/hostagent/...
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh hostagent
file bin/moox-host-agent
```

Expected: tests pass and file reports an x86-64 Linux executable.

- [ ] **Step 2: Back up and deploy to `106.53.107.122`**

Create a timestamped release under `~/.local/lib/moox/hostagent/releases/`, copy the binary and current config, set `host_name: "腾讯云-122"`, switch `current` atomically, reload user systemd, restart, and verify both `is-active` and the existing health endpoint. Preserve EventBus config and identity paths.

- [ ] **Step 3: Back up and deploy to `43.132.204.177`**

Repeat Step 2 with `host_name: "腾讯云-香港"`. Preserve its existing EventBus tunnel and health configuration.

- [ ] **Step 4: Prove continuous publication**

On each host, record `systemctl --user status`, health output, and journal lines after at least two 15-second collection intervals. Expected: service remains active and no repeating publish error appears.

## Task 6: Production Data and Browser Acceptance

**Files:** modify only if live verification exposes a concrete defect; add a regression test before fixing it.

- [ ] **Step 1: Verify current Monitor data**

Use the authenticated production control API or browser page to prove that `ListHostAgents` returns both `腾讯云-122` and `腾讯云-香港`, both fresh and with CPU, memory, filesystem, disk, and network snapshots.

- [ ] **Step 2: Verify history after two collection cycles**

Query `QueryHostMetricHistory` for each agent. Expected: each host has multiple timestamped points and the latest timestamp advances after waiting at least 30 seconds.

- [ ] **Step 3: Verify desktop and mobile browser behavior**

At `https://106.53.107.122:9527/#/ops/hosts?tab=monitor`, verify card view is the first-use default, both hosts show their SSH IP addresses and real metrics, selection opens history/device details, master view retains selection, the legacy route redirects to the monitor Tab, bottom tables are reachable, and 1440x900, 1280x720, and 390x844 layouts have no overlap.

- [ ] **Step 4: Push and audit completion**

```bash
git push
git status --short --branch
git rev-parse HEAD
git rev-parse origin/feature/frontend-service-host-workbench
```

Expected: working tree clean and local HEAD equals the remote feature branch. Do not claim completion until both hosts have fresh current metrics, queryable history, and visible production UI data.
