# MooX Frontend Service and Host Workbench Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Reorganize the MooX frontend into Service Management and Host Workbench pages, simplify view browsing, and make service-monitoring pages scrollable and consistent.

**Architecture:** Keep Monitor, SysDeploy, SSH, Storage, and metric APIs unchanged. Add thin PageTitleTabs route shells, move existing views behind them, add a pure Monitor/SSH/session merge helper, and extract the SSH terminal into a reusable modal surface. Use one explicit page scroll owner and bounded table scrolling for detail panes.

**Tech Stack:** Vue 3, TypeScript, Vue Router 4, Pinia, Arco Design Vue, Vitest, Playwright, Vite, embedded web-host statik assets.

---

## File Map

Create:

- web/src/views/ops/service-management/index.vue: /ops/services shell and three tabs.
- web/src/views/ops/service-management/service-management-tabs.ts and .test.ts: typed tab parsing/normalization.
- web/src/views/ops/host-workbench/index.vue: /ops/hosts shell.
- web/src/views/ops/host-workbench/host-list.vue: unified host table, detail, and terminal launch.
- web/src/views/ops/host-workbench/host-sessions.vue: online session tab.
- web/src/views/ops/host-workbench/host-workbench-mapping.ts and .test.ts: deterministic Monitor/SSH/session mapping.
- web/src/views/container/ssh-terminal/terminal-workspace.vue: reusable xterm body.
- web/src/views/data/view-browse/view-build-display.ts and .test.ts: active build-time selection.

Modify:

- web/src/api/modules/system/static-menu.ts, web/src/lang/modules/zhCN.ts, web/src/lang/modules/enUS.ts: menu names, icons, and collapsed Ops entries.
- web/src/router/route.ts: canonical workbench routes and hidden redirects.
- web/src/views/data/view-browse/index.vue: remove redundant controls/count and show build time.
- web/src/views/ops/service-monitor/index.vue: child layout, page scrolling, table and drawer fixes.
- web/src/views/ops/metric-monitor/index.vue: detail-page layout, full-width table then chart.
- web/src/views/settings/service-deployments/index.vue: remove standalone shell; render as Service Management child.
- web/src/views/container/resource-monitor/resource-monitor.vue, ssh-hosts/ssh-hosts.vue, ssh-sessions/ssh-sessions.vue, ssh-terminal/ssh-terminal.vue: move reusable behavior behind Host Workbench and remove standalone navigation assumptions.
- web/scripts/check-menu-structure.mjs, check-detail-page-style.mjs, check-host-monitor-contract.mjs: enforce new contracts.

Inspect only:

- web/src/components/page-title-tabs/index.vue
- web/src/style/model/global-style.scss
- web/src/api/modules/host-monitor.ts
- web/src/api/modules/ssh.ts
- web-host/internal/statik/statik.go

Do not reset or stage unrelated dirty files already present. Use CI=true and go test -count=1 for fresh proof.

## Task 1: Contracts, menu, and route helpers

**Files:** new service-tab/build-time helpers and tests; modify static menu, translations, routes, check-menu-structure.mjs.

- [ ] Step 1: Add failing tab tests. Require parseServiceTab(undefined or invalid) == deployments, valid metrics/availability, parseHostTab(undefined or invalid) == hosts, and canonical query objects with tab.
- [ ] Step 2: Run CI=true pnpm -C web vitest run src/views/ops/service-management/service-management-tabs.test.ts. Expected FAIL because the helper is absent.
- [ ] Step 3: Implement typed helpers exporting ServiceTab, HostTab, parsers with defaults, and query builders.
- [ ] Step 4: Add failing build-display tests for active-index/finished_at, started_at, mismatched index, invalid timestamp, no active index, and no view.updated_at fallback.
- [ ] Step 5: Implement selectViewBuildDisplay(view), matching index_build.index_id to active_index_id and returning completed, building, unknown, or hidden.
- [ ] Step 6: Update menu/routes. Use icon-experiment for factor and icon-mind-mapping for strategy. Visible Ops children become ops-services, ops-host-workbench, and ops-storage. Move service deployments out of settings. Add hidden redirects:

~~~text
/settings/service-deployments -> /ops/services?tab=deployments
/ops/service-monitor -> /ops/services?tab=availability
/ops/metric-monitor -> /ops/services?tab=metrics
/ops/resource-monitor -> /ops/hosts?tab=hosts
/ops/ssh-hosts -> /ops/hosts?tab=hosts
/ops/ssh-sessions -> /ops/hosts?tab=sessions
/ops/ssh-terminal -> /ops/hosts?tab=hosts
~~~

- [ ] Step 7: Extend check-menu-structure.mjs to assert three visible Ops children, no settings service deployment, distinct icons, and all new route strings.
- [ ] Step 8: Verify and commit with the focused Vitest command, CI=true pnpm -C web check:menu, then commit message refactor(web): define service and host workbench routes.

## Task 2: Service Management shell

**Files:** create web/src/views/ops/service-management/index.vue; modify service deployment, service monitor, and metric monitor pages.

- [ ] Step 1: Add a shell contract requiring tabs 服务部署, 可用性监控, 应用指标 and default deployments.
- [ ] Step 2: Implement one moox-page, one moox-inner, PageTitleTabs, route.query.tab, and router.replace to /ops/services with the canonical query; render child by tab and remove duplicate child H2 headers.
- [ ] Step 3: Adapt service deployments, preserving list, filters, CRUD, health fallback, and storage warning while removing conflicting standalone shell.
- [ ] Step 4: Mount availability and metrics children without changing APIs.
- [ ] Step 5: Verify with CI=true pnpm -C web vue-tsc --noEmit and commit refactor(web): add service management workbench.

## Task 3: Simplify view browse

**Files:** web/src/views/data/view-browse/index.vue and build-display helper/tests.

- [ ] Step 1: Add a contract requiring no visible header 刷新, 重新查询, or 已加载 text while query-panel 查询, 清空, pagination, preview pager, K-line, and detail modal remain.
- [ ] Step 2: Remove only the two header buttons and loaded-count span.
- [ ] Step 3: Render completed as 构建完成于 text, building as 构建中 · 开始于 text, and unknown as 构建时间未知. Never fall back to activeView.updated_at.
- [ ] Step 4: Run the build-display test and CI=true pnpm -C web check:data-browse. Commit refactor(web): simplify view browse status.

## Task 4: Normalize service availability and application metrics

**Files:** service-monitor/index.vue, metric-monitor/index.vue, service-deployments/index.vue, check-detail-page-style.mjs.

- [ ] Step 1: Add contracts requiring latest metric table before history chart, flat filter band, no two-column explorer cards, service page scroll ownership, and bounded detail-table scroll.
- [ ] Step 2: Flatten metrics: keep existing APIs, 50-series/10-chart caps, stale/partial/error/retry states, rules/editor/evaluations; replace explorer-grid with full-width latest table followed by chart; use compact 指标浏览/告警规则 control.
- [ ] Step 3: Put availability child inside moox-page semantics with height:100%, min-height:0, overflow-y:auto. Keep summary, failures, filters, table, drawers.
- [ ] Step 4: Fix detail drawer with explicit flex height chain. Set detail body to display:flex, min-height:0, flex-direction:column; make Arco tabs and content flex; make each pane overflow:auto; bound detail-table to min(360px, calc(100vh - 380px)); keep fixed headers and horizontal scroll.
- [ ] Step 5: Keep one primary table action visible and move secondary actions to an Arco dropdown without changing behavior.
- [ ] Step 6: Run CI=true pnpm -C web check:detail-pages and check:metric-monitor. Commit fix(web): normalize service monitoring layouts.

## Task 5: Unified Host Workbench mapping and UI

**Files:** create host-workbench shell/list/sessions/mapping/tests; modify host-monitor and SSH type exports only if needed.

- [ ] Step 1: Write tests for exact address match, unique-name match, ambiguous names, Monitor-only rows, SSH-only rows, and session aggregation.
- [ ] Step 2: Run CI=true pnpm -C web vitest run src/views/ops/host-workbench/host-workbench-mapping.test.ts. Expected FAIL because helper is absent.
- [ ] Step 3: Implement HostWorkbenchRow with key, optional monitor HostMetrics, optional ssh SSHHost, sessions SessionInfo array, and match address/unique_name/unmatched. Export mergeHostWorkbenchRows(monitors, sshHosts, sessions).
- [ ] Step 4: Normalize whitespace/case/trailing dot only; never fuzzy-match, substring-match, or reverse-resolve. Preserve ambiguous rows and show 未配置 SSH/未接入监控.
- [ ] Step 5: Load Monitor, SSH, and sessions independently with Promise.allSettled. Show host status, resource summaries, SSH user/port, session count, last report, view/connect/edit/delete, and 1h/24h/3d detail tables.
- [ ] Step 6: Use parseHostTab, router.replace, and optional hostId selection; connection opens modal, never standalone route.
- [ ] Step 7: Move online sessions, preserving auto-refresh and force-disconnect, adding host filter and return-to-host action.
- [ ] Step 8: Run focused mapping tests and commit feat(web): add unified host workbench.

## Task 6: SSH terminal modal extraction

**Files:** create terminal-workspace.vue; modify old terminal page and host list/shell.

- [ ] Step 1: Extract current xterm template/lifecycle into terminal-workspace.vue with initialHostId and close emit; keep multi-tab, AttachAddon, FitAddon, resize, reconnect, clear, disconnect, SFTP, and cleanup.
- [ ] Step 2: Add lifecycle regression coverage: opening calls createSSHSession, closing disconnects modal-owned sessions, teardown releases WebSocket/xterm/ResizeObserver.
- [ ] Step 3: Mount an Arco modal with width min(1200px, 92vw), footer false, unmount-on-close, and stable body height. Use a right drawer for file management; confirm before disconnecting active modal sessions.
- [ ] Step 4: Redirect old terminal route to /ops/hosts?tab=hosts&hostId=... and remove visible menu entry.
- [ ] Step 5: Run CI=true pnpm -C web vue-tsc --noEmit and commit feat(web): open ssh terminals from host workbench.

## Task 7: Contract checks and full local verification

**Files:** check-menu-structure.mjs, check-detail-page-style.mjs, check-host-monitor-contract.mjs, and focused tests only where coverage is missing.

- [ ] Step 1: Assert canonical service/host routes, redirects, three visible Ops children, and no visible legacy Ops entries.
- [ ] Step 2: Assert ListHostAgents, ListHosts, GetOnlineSessions, connection aria-label, mapping import, min-height:0, and table scroll.
- [ ] Step 3: Assert latest-table-before-history, service drawer overflow classes, and no duplicate child H2.
- [ ] Step 4: Run CI=true pnpm -C web check:menu, check:data-browse, check:metric-monitor, check:host-monitor, and check:detail-pages; all must exit 0.
- [ ] Step 5: Run CI=true pnpm -C web vitest run --config vitest.config.ts, CI=true pnpm -C web vue-tsc --noEmit, and CI=true pnpm -C web build:prod; all must pass.
- [ ] Step 6: Use Playwright at 1440x900, 1280x720, and 390x844 to verify view browse, all Service Management tabs, Host Workbench, bottom-table scrolling, drawer panes, responsive non-overlap, and screenshots.
- [ ] Step 7: With an SSH fixture, verify modal visibility, non-zero xterm canvas, resize fit, and cleanup request.
- [ ] Step 8: Commit only concrete verification fixes with a focused message and rerun the failing check.

## Task 8: Independent review agent

- [ ] Step 1: Capture git diff origin/main...HEAD --stat, git status --short --branch, and fresh menu/detail/test outputs.
- [ ] Step 2: Spawn a fresh review agent with this plan, design spec, final diff, screenshots, and test output. Request only concrete bugs/regressions/missing acceptance coverage, ordered by severity with file/line references; focus on routes/menu, scroll ownership, host identity merges, and terminal lifecycle.
- [ ] Step 3: For every finding, add a regression test, reproduce, make the smallest fix, rerun focused checks, and commit. Do not proceed with P0/P1 findings open.

## Task 9: Embed, publish, and live verification

- [ ] Step 1: After verified build, run the existing frontend/statik generation workflow and confirm web-host/internal/statik/statik.go contains new route/menu strings.
- [ ] Step 2: Run go test -count=1 ./web-host/...; expected PASS.
- [ ] Step 3: Build Linux web-host with the existing TARGET_GOOS=linux command.
- [ ] Step 4: Publish through the existing MooX deployment workflow, preserving Admin Gateway/Caddy CA workflow and using Admin Gateway rather than web-host port 9527 for API validation.
- [ ] Step 5: Verify deployed service deployments/availability/metrics, hosts, old redirects, /#/collector/views?tab=browse, build time, bottom-table scrolling, SSH WebSocket open/close, and real API data.
- [ ] Step 6: Prove sync with git status --short --branch, git rev-parse HEAD, and git rev-parse origin/main; expected HEAD equals origin/main and unrelated pre-existing changes remain untouched.

## Final Acceptance

- [ ] Design spec and this plan are committed and pushed.
- [ ] Menu/routes, Service Management, Host Workbench, view-browse, scrolling, metrics style, and SSH modal requirements are implemented.
- [ ] Fresh unit, contract, typecheck, build, web-host, and browser checks pass.
- [ ] A fresh review agent reviewed the final diff and all findings are resolved.
- [ ] Embedded web-host is published and live browser/API verification succeeds.
- [ ] HEAD equals origin/main.
