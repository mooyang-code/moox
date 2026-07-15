# Collector Task Page Style Alignment Design

## Goal

Align `/#/collector/tasks` with the established list-page presentation used by `/#/collector/packages` while preserving task filtering, pagination, detail viewing, and API behavior.

## Scope

This change covers the task list surface and the minimum detail-modal polish needed for visual consistency. It does not change task APIs, field meanings, pagination semantics, task status values, or detail content.

## Page Structure

- Keep the standard `moox-page > moox-inner` page shell.
- Remove the standalone `任务实例` heading because the route breadcrumb and navigation already identify the page, matching the package page.
- Remove the bordered `task-result-pane` wrapper so the table sits directly in the content area.
- Replace the separate query section with a `task-toolbar` that follows the package page's flex, wrapping, gap, and bottom-spacing rules.
- Let the document provide vertical scrolling instead of fixing the table body to `500px`.
- Retain horizontal table scrolling for narrow viewports because the task table contains operational columns that must not be compressed into unreadable widths.

## Controls

- Keep all current filters and their existing request mapping.
- Style the filter group like `package-filters`.
- Keep `查询` as a primary button with the search icon.
- Keep `重置` as a default button with the refresh icon.
- Preserve the effective/deleted switch and its behavior.
- Remove row-selection checkboxes and the associated local state and handlers because no bulk operation consumes the selection.

## Table

- Use the same small, cell-bordered table presentation as the package page.
- Render task IDs as zero-padding text buttons, matching clickable package names.
- Keep tooltips and ellipsis behavior for long identifiers and node names.
- Keep semantic status colors, but remove the extra bordered-tag treatment so tags match the package page.
- Render the row-level `查看` command as a primary mini button with the standard view icon.
- Preserve column data, fixed operation placement, pagination, page-size changes, and detail opening behavior.

## Detail Modal

- Preserve all current task fields and JSON sections.
- Keep the modal read-only and footerless.
- Apply only spacing and code-block styling needed to remain consistent with the list page and existing MooX detail surfaces.

## Responsive Behavior

- The toolbar wraps filters without overlapping controls.
- The table keeps stable column widths and horizontal overflow on constrained screens.
- No fixed vertical table height is used, so the page follows the application's normal scrolling behavior.

## Verification

- Add a task-page structure contract that requires the aligned toolbar, table configuration, task-ID text button, and primary mini detail action.
- The contract rejects the removed page heading, result card, row selection, and fixed table-body height.
- Run the contract in RED before implementation and GREEN afterward.
- Run Vue type checking, the frontend test suite, production build, `web-host` embedding tests, and browser verification on desktop and narrow viewports.
- Confirm filters, reset, pagination, task detail opening, horizontal overflow, and console logs in the deployed page.

## Non-Goals

- No shared list-page abstraction is introduced in this change.
- No new bulk task operations are added.
- No task API, backend, route, or data-model changes are included.
