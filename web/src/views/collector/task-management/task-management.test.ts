import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const normalizeSource = (source: string) => source.replace(/\s+/g, "").replace(/'/g, '"');

describe("collector task management workbench", () => {
  it("keeps the five tabs in tasks, results, status, executors, backfills order", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    const positions = ["采集任务", "采集结果", "运行状态", "执行器", "补采"].map(label => normalized.indexOf(`label:"${label}"`));

    expect(source).toContain("PageTitleTabs");
    expect(source).toContain('aria-label="采集任务"');
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
    expect(normalized).toContain("executors:CloudNode");
    expect(normalized).toContain("status:RuntimeStatus");
    expect(normalized).toContain("backfills:BackfillJobs");
    expect(normalized).toContain('typeCollectorTaskTab="tasks"|"status"|"backfills"|"executors"|"results"');
  });

  it("uses the result task query only for the result tab", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    expect(normalized).toContain('tab==="results"&&resultTask?{tab,resultTask}:{tab}');
    expect(normalized).toContain("consttab=normalizeTab(value)");
  });

  it("keeps one visible task menu and removes retired collector URLs", () => {
    const menu = fs.readFileSync(path.resolve(__dirname, "../../../api/modules/system/static-menu.ts"), "utf8");
    const routes = fs.readFileSync(path.resolve(__dirname, "../../../router/route.ts"), "utf8");
    const normalizedMenu = normalizeSource(menu);
    const normalizedRoutes = normalizeSource(routes);

    expect(normalizedMenu).toContain('menu("0303","03","/collector/tasks","collector-tasks"');
    expect(normalizedMenu).not.toContain('menu("0304"');
    expect(normalizedRoutes).toContain('component:()=>import("@/views/collector/task-management/index.vue")');
    expect(normalizedRoutes).toContain('path:"/collector/tasks"');
    for (const retired of ["/collector/rules", "/collector/data-management", "/collector/datasets", "/collector/packages"]) {
      expect(normalizedRoutes).not.toContain(`path:"${retired}"`);
    }
  });

  it("keeps the create action in the task search row", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "../collection-tasks/collection-tasks.vue"), "utf8");
    const firstToolbarEnd = source.indexOf("</a-space>");
    const tableStart = source.indexOf("<a-table");
    const createPosition = source.indexOf("新建采集任务");
    const searchPosition = source.indexOf("查询");

    expect(createPosition).toBeGreaterThan(0);
    expect(createPosition).toBeLessThan(searchPosition);
    expect(createPosition).toBeLessThan(firstToolbarEnd);
    expect(source.slice(firstToolbarEnd, tableStart)).not.toContain("新建采集任务");
  });

  it("lets the runtime status tab fill the height and scroll inside its function list", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(source).toContain('const fillHeight = computed(() => activeTab.value === "status")');
    expect(source).toContain("'task-management-content--fill': fillHeight");
    expect(source).toMatch(/\.collector-task-management-page--fill > \.moox-inner\s*\{[^}]*height:\s*100%;/);
    expect(source).toMatch(/\.task-management-content--fill\s*\{[^}]*overflow-y:\s*hidden;/);
  });

  it("derives the active tab from the URL only", () => {
    const normalized = normalizeSource(fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8"));
    expect(normalized).toContain("constactiveTab=computed<CollectorTaskTab>(()=>normalizeTab(route.query.tab))");
    expect(normalized).not.toMatch(/activeTab\.value=(?!=)/);
  });
});
