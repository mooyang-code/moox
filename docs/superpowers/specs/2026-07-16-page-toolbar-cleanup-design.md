# Page Toolbar Cleanup Design

## Goal

Simplify MooX administration pages by removing redundant inline status copy, warning rows, counters, and refresh or reset actions that compete with primary create and query workflows.

## Chosen Approach

Edit each affected Vue template explicitly and add a repository-level contract test for the shared toolbar rule. Do not hide controls with global CSS and do not introduce a new toolbar abstraction for this visual cleanup.

## Page Changes

### Host Resource Monitor

- Rename `主机资源状态` to `资源状态`.
- Remove the relative update label, including `刚刚更新`, the first-update placeholder, and their unused formatting state.
- Keep manual refresh, automatic refresh, and view-mode controls because these operate live monitoring rather than a create or query workflow.

### Service Instances

- Remove the refresh button beside `新增实例`.
- Remove the static storage warning alert and any persistent inline error alert row from the content area.
- Keep form validation and API failures visible through transient Tips messages.
- Reduce the management content top spacing and the filter-to-table spacing to `12px`, matching the compact collector rules rhythm.

### Secret Management

- Remove the explanatory subtitle.
- Remove the refresh button beside `新增秘钥`.
- Place the keyword search in the title row with the page title and green create button, following the data-source page pattern.
- Keep category and status selectors in a separate compact filter row.

### Field Management

- Remove the field-count label and refresh button.
- Add a blue query button with a search icon beside the keyword and filter controls.
- Keep Enter in the keyword input as a query trigger.
- Keep the green create button in the action area.

### Data Sources

- Remove the refresh button beside `新增来源`.
- Preserve the existing title-row search and create layout.

## Repository-Wide Toolbar Rule

Audit all Vue pages and embedded workbench panels. When a refresh or reset action appears in the same toolbar as a create action or query action, remove the refresh or reset action and its now-unused handler only when no other caller needs it.

The rule includes collector rules, cloud-node searches, package management searches, SSH host searches, data metadata list pages, factor list pages, settings list pages, and embedded metadata panels when their toolbar has the same create/query-plus-refresh pattern.

The rule excludes independent operational refresh actions such as live monitoring, trading balances and positions, file browsers, detail views, chart data, and other controls whose toolbar has no create or query action.

## Behavior And Error Feedback

- Data loading still occurs on mount, pagination, filter changes, successful mutations, and explicit query actions where already supported.
- Removing a visible refresh or reset button must not remove load functions still used by lifecycle hooks or mutations.
- Service-instance API errors remain reported by the existing transient message mechanism; only persistent horizontal alert rows are removed.
- No API contracts, routes, data models, or modal behavior change.

## Verification

- Add focused contract tests for the five named pages.
- Add a repository-wide source contract that rejects refresh or reset controls adjacent to create or query controls, with explicit operational exceptions.
- Run the focused tests red before implementation and green after implementation.
- Run the full frontend unit suite and production build.
- Use Playwright to verify the named live pages, desktop spacing, visible copy, and button sets.
- Regenerate embedded assets, deploy `web-host`, compare local and remote binary hashes, and confirm the live health check.
