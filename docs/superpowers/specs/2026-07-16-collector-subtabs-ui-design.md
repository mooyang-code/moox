# Collector Secondary Tabs UI Design

## Goal

Make the `视图定义 / 查看数据` secondary navigation read as page navigation rather than filter tags. Keep it visually subordinate to the `数据视图` page title and aligned with the table content.

## Considered Approaches

1. Keep the current pill shape and only reduce padding. This is the smallest change, but the control still resembles a tag or filter.
2. Use bordered segmented controls. This makes the grouping explicit, but adds too much visual weight beside the page actions.
3. Use compact rounded rectangles with a filled active state. This preserves clear selection while matching the restrained admin interface.

Use approach 3.

## Visual Contract

- Keep the secondary tabs at `28px` high.
- Replace the pill radius with a `4px` corner radius.
- Use a light neutral background and primary blue text for the active tab.
- Keep inactive tabs transparent with normal secondary text.
- Do not add an outer border or shared container background.
- Preserve the current left alignment and compact vertical spacing.
- Apply the same treatment to the data view and data collection secondary tabs because they share one workbench pattern.

## Scope

Only the `collector-subtabs` controls inside the collector data-management workbench change. The main title tabs, header space launcher, buttons, routing, and data behavior remain unchanged.

## Verification

- Add a style-contract regression assertion for the rectangular radius and active state.
- Run the full frontend unit suite and production build.
- Verify desktop geometry and appearance in Playwright.
- Publish the updated embedded frontend and confirm the live `web-host` health check.
