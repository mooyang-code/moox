# Service Management Layout Polish Design

## Goal

Align `/#/ops/services` with the approved host-workbench and data-view page hierarchy without changing service-management behavior.

## Approved Layout

- Replace the standalone "服务管理" heading, description, and rounded page-level tabs with the shared `PageTitleTabs` component.
- Keep three title tabs: "服务部署", "可用性监控", and "应用指标".
- Preserve the existing route values: `deployments`, `availability`, and `metrics`.
- Place embedded page content 16px below the title tabs.
- Remove nested `moox-page` and `moox-inner` padding, borders, backgrounds, and shadows from embedded children.
- Keep each child page's actions, filters, status blocks, tables, drawers, and modals unchanged.
- Keep the "指标看板 / 告警规则" tabs inside the application-metrics page as secondary navigation.

## Responsive And Interaction Rules

- Reuse `PageTitleTabs`; do not copy its CSS.
- Preserve keyboard and ARIA behavior supplied by `PageTitleTabs`.
- Keep `keep-alive` behavior and route synchronization.
- Do not add new cards, animation, or decorative surfaces.

## Verification

- Add a service-management contract that requires `PageTitleTabs` and forbids the old heading and rounded page-level tabs.
- Run frontend tests, type checking, detail-page checks, the new contract, and production build.
- Regenerate statik assets, run web-host Go tests, deploy only web-host, and verify all three production tabs in a browser with no console errors.
