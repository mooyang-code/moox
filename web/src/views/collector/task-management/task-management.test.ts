import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const normalizeSource = (source: string) => source.replace(/\s+/g, "").replace(/'/g, '"');

describe("collector task management workbench", () => {
  it("combines collection rules and task instances in one ordered tab surface", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    const positions = ["采集规则", "任务实例"].map(label => normalized.indexOf(`label:"${label}"`));

    expect(source).toContain("PageTitleTabs");
    expect(source).toContain('aria-label="采集任务"');
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
    expect(normalized).toContain('typeCollectorTaskTab="rules"|"instances"');
  });

  it("keeps one visible menu and redirects the old task URL to its tab", () => {
    const menu = fs.readFileSync(path.resolve(__dirname, "../../../api/modules/system/static-menu.ts"), "utf8");
    const routes = fs.readFileSync(path.resolve(__dirname, "../../../router/route.ts"), "utf8");
    const normalizedMenu = normalizeSource(menu);
    const normalizedRoutes = normalizeSource(routes);

    expect(normalizedMenu).toContain('menu("0303","03","/collector/rules","collector-rules"');
    expect(normalizedMenu).not.toContain('menu("0304"');
    expect(normalizedRoutes).toContain('component:()=>import("@/views/collector/task-management/index.vue")');
    expect(normalizedRoutes).toContain('redirect:{path:"/collector/rules",query:{tab:"instances"}}');
  });

  it("removes the standalone package page and keeps package management on cloud nodes", () => {
    const menu = fs.readFileSync(path.resolve(__dirname, "../../../api/modules/system/static-menu.ts"), "utf8");
    const routes = fs.readFileSync(path.resolve(__dirname, "../../../router/route.ts"), "utf8");
    const cloudNodes = fs.readFileSync(path.resolve(__dirname, "../cloud-node/cloud-node.vue"), "utf8");
    const normalizedMenu = normalizeSource(menu);
    const normalizedRoutes = normalizeSource(routes);
    const normalizedCloudNodes = normalizeSource(cloudNodes);

    expect(normalizedMenu).not.toContain('menu("0302"');
    expect(normalizedRoutes).toContain('path:"/collector/packages"');
    expect(normalizedRoutes).toContain('redirect:"/collector/cloudnodes"');
    expect(normalizedRoutes).not.toContain('component:()=>import("@/views/collector/cloud-node/function-package-manage.vue")');
    expect(normalizedCloudNodes).toContain('importFunctionPackageManagefrom"./function-package-manage.vue"');
  });

  it("places the create action in the collection rule search row", () => {
    const rules = fs.readFileSync(path.resolve(__dirname, "../collector-rules/collector-rules.vue"), "utf8");
    const firstToolbarEnd = rules.indexOf("</a-space>");
    const tableStart = rules.indexOf("<a-table");
    const createPosition = rules.indexOf("<span>新建任务</span>");
    const searchPosition = rules.indexOf("<span>查询</span>");

    expect(createPosition).toBeGreaterThan(0);
    expect(createPosition).toBeLessThan(searchPosition);
    expect(createPosition).toBeLessThan(firstToolbarEnd);
    expect(rules.slice(firstToolbarEnd, tableStart)).not.toContain("新建任务");
  });
});
