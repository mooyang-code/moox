# Host And Service Toolbar Alignment Design

## Goal

Make the default host and service list pages share one compact action-first toolbar pattern.

## Scope

Change only the default `主机列表` page under `/ops/hosts` and the default `网关节点` page under `/ops/services`. Do not restructure host monitoring, service instances, availability monitoring, or application metrics.

## Host List

- Merge the existing search row and action row into one wrapping toolbar.
- Order controls as `新增主机`, keyword search, `查询`, and `批量删除`.
- Keep the green create style, blue query style, existing Enter and clear search behavior, selection state, and batch-delete behavior.
- Remove the now-unneeded nested row wrapper.

## Gateway Nodes

- Merge the split filter and action groups into one wrapping toolbar.
- Order controls as `新增节点`, node ID search, status filter, and `查询`.
- Keep the green create style and existing filter behavior.

## Spacing And Responsive Behavior

- Set the main-title-to-content spacing to `12px` on both workbenches.
- Set the toolbar-to-table spacing to `12px` on both list pages.
- Use wrapping flex or Arco Space behavior so controls flow naturally on narrow screens instead of becoming separate action and search rows.
- Preserve stable input widths and existing table dimensions.

## Implementation Approach

Edit the two child list components explicitly and align their parent content spacing. Do not lift query state into parent workbench components and do not introduce a new shared toolbar component for this small layout change.

## Verification

- Add source contract assertions for control order, single-toolbar structure, and `12px` spacing.
- Run the contract red before implementation and green after implementation.
- Run the complete frontend unit suite and production build.
- Verify both default routes in Playwright at desktop and narrow widths.
- Regenerate embedded assets, deploy `web-host`, and confirm live layout, health, binary hash, and Git synchronization.
