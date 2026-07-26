import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const normalizeSource = (source: string) => source.replace(/\s+/g, "").replace(/'/g, '"');

describe("collector data management workbench", () => {
  it("combines data views and data collections in the recommended order", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    const viewPosition = normalized.indexOf('{key:"views",label:"数据视图"}');
    const datasetPosition = normalized.indexOf('{key:"datasets",label:"数据集合"}');

    expect(source).toContain("PageTitleTabs");
    expect(source).toContain('aria-label="数据管理"');
    expect(viewPosition).toBeGreaterThan(0);
    expect(datasetPosition).toBeGreaterThan(viewPosition);
    expect(source).toContain("<CollectorViews");
    expect(source).toContain("<CollectorDatasets");
  });

  it("uses a compact rounded style for the secondary workbench tabs", () => {
    const management = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const datasets = fs.readFileSync(path.resolve(__dirname, "../datasets/index.vue"), "utf8");
    const views = fs.readFileSync(path.resolve(__dirname, "../views/index.vue"), "utf8");

    for (const source of [datasets, views]) {
      expect(source).toContain('type="rounded"');
      expect(source).toContain('size="small"');
      expect(source).toContain('class="collector-subtabs"');
      expect(source).not.toContain("PageTitleTabs");
    }

    expect(management).toMatch(/\.management-content\s*\{[\s\S]*?margin-top:\s*var\(--moox-space-3\);/);
    expect(management).toMatch(/:deep\(\.page-head\)\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-2\);/);
    expect(management).toMatch(/:deep\(\.collector-subtabs \.arco-tabs-content\)\s*\{[\s\S]*?display:\s*none;/);
    expect(management).toMatch(/:deep\(\.collector-subtabs \.arco-tabs-tab:first-child\)\s*\{[\s\S]*?margin-left:\s*0;/);
    expect(management).toMatch(/:deep\(\.collector-subtabs \.arco-tabs-tab\)\s*\{[\s\S]*?border-radius:\s*4px;/);
    expect(management).toMatch(
      /:deep\(\.collector-subtabs \.arco-tabs-tab-active\)\s*\{[\s\S]*?color:\s*rgb\(var\(--primary-6\)\);/
    );
    expect(management).toMatch(
      /:deep\(\.collector-subtabs \.arco-tabs-tab-active\)\s*\{[\s\S]*?background-color:\s*var\(--color-fill-2\);/
    );
  });

  it("exposes only the unified data-management route", () => {
    const menu = fs.readFileSync(path.resolve(__dirname, "../../../api/modules/system/static-menu.ts"), "utf8");
    const routes = fs.readFileSync(path.resolve(__dirname, "../../../router/route.ts"), "utf8");
    const normalizedMenu = normalizeSource(menu);
    const normalizedRoutes = normalizeSource(routes);

    expect(normalizedMenu).toContain('menu("0305","03","/collector/data-management","collector-data-management"');
    expect(normalizedMenu).not.toContain('menu("0306"');
    expect(normalizedRoutes).toContain('component:()=>import("@/views/collector/data-management/index.vue")');
    expect(normalizedRoutes).not.toContain('path:"/collector/datasets"');
    expect(normalizedRoutes).not.toContain('path:"/collector/views"');
    expect(normalizedRoutes).not.toContain('path:"/data/datasets"');
    expect(normalizedRoutes).not.toContain('path:"/data/views"');
  });
});
