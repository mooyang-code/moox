# Host Workbench Layout Polish Design

## Goal

Polish the host workbench so it follows the same page hierarchy and spacing conventions as `/#/collector/views`, while keeping the monitoring workflow dense and readable.

## Approved Direction

Use the approved B1 layout:

- Replace the standalone "主机工作台" heading, description, and rounded Arco tabs with the shared `PageTitleTabs` title-level navigation.
- Keep two title tabs: "主机列表" and "主机监控".
- Preserve the existing route contract: `tab=hosts` and `tab=monitor`.
- Keep the monitor toolbar directly below the title tabs with the existing refresh, automatic refresh, and view-mode controls.

This mirrors the hierarchy used by `/#/collector/views`, where the title-level tabs are the page title rather than a second navigation layer below a heading.

## Spacing And Surfaces

- Keep the global `moox-page` and `moox-inner` shell; do not add another card around the page or detail region.
- Use the same inner-page rhythm as other detail pages:
  - 16px between title tabs and the monitor toolbar.
  - 10-12px between toolbar, summary, alerts, and primary content.
  - 20px before the selected-host detail section.
  - 18-20px between detail subsections.
- The selected-host detail remains an unframed section separated by a single top border.
- Do not add decorative backgrounds, oversized headings, or nested cards.

## Detail Layout

- Keep the current selected-host header, time-range control, trend chart, and device overview.
- Preserve the two-column trend/overview layout on desktop and stack it on narrow screens.
- Keep the filesystem, disk I/O, and network tables in a three-column desktop grid.
- At widths below the existing detail breakpoint, stack the three tables vertically.

## Small Table Behavior

Remove desktop horizontal scrolling from these tables:

- 文件系统
- 磁盘 I/O
- 网络接口

Implementation rules:

- Remove the fixed `min-width: 460px` table constraint.
- Remove horizontal `overflow: auto` from the table wrapper.
- Use `table-layout: fixed`, compact cell padding, and ellipsis/title disclosure for long values.
- Give important identity columns slightly more room through table-specific column widths.
- Keep the vertical maximum height and sticky headers.
- On narrow viewports, stack table sections rather than introducing horizontal scrolling.

## Interaction And Accessibility

- Reuse `PageTitleTabs`; do not create a second tab implementation.
- Preserve keyboard focus and ARIA semantics supplied by `PageTitleTabs` and existing controls.
- Keep frequent interactions effectively instant; no new entrance animations.
- Retain existing loading, warning, empty, online, offline, and attention states.

## Verification

- Contract test asserts that the host workbench uses `PageTitleTabs` and no rounded page-level tabs remain.
- Contract test asserts that small tables no longer declare a fixed minimum width or horizontal overflow.
- Run frontend unit tests, type checking, detail-page style checks, host-monitor contract checks, and production build.
- Regenerate embedded web assets and run `web-host` Go tests.
- Deploy the production web host and verify in a browser:
  - title tabs match the data-view style;
  - only the two expected tabs are present;
  - detail margins align with the page shell;
  - all three small tables fit without horizontal scroll at the production desktop viewport;
  - the browser console has no errors.
