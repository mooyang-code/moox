import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const normalizeSource = (source: string) => source.replace(/\s+/g, "").replace(/'/g, '"');

describe("collector task management workbench", () => {
  it("keeps the four tabs in tasks, instances, executors, results order", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    const positions = ["采集任务", "任务实例", "执行器", "采集结果"].map(label => normalized.indexOf(`label:"${label}"`));

    expect(source).toContain("PageTitleTabs");
    expect(source).toContain('aria-label="采集任务"');
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
    expect(normalized).toContain("executors:CloudNode");
    expect(normalized).toContain('typeCollectorTaskTab="tasks"|"instances"|"executors"|"results"');
  });

  it("uses the result task query only for the result tab", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    expect(normalized).toContain('tab:"results"');
    expect(normalized).toContain("resultTask");
    expect(normalized).toContain("resultTask:undefined");
  });

  it("redirects the legacy cloud-node entry into the executor tab", () => {
    const menu = fs.readFileSync(path.resolve(__dirname, "../../../api/modules/system/static-menu.ts"), "utf8");
    const routes = fs.readFileSync(path.resolve(__dirname, "../../../router/route.ts"), "utf8");
    const home = fs.readFileSync(path.resolve(__dirname, "../../home/home.vue"), "utf8");
    const normalizedMenu = normalizeSource(menu);
    const normalizedRoutes = normalizeSource(routes);

    expect(normalizedMenu).not.toContain('menu("0301"');
    expect(normalizedRoutes).toContain('path:"/collector/cloudnodes"');
    expect(normalizedRoutes).toContain('path:"/collector/tasks",query:{...to.query,tab:"executors"}');
    expect(home).toContain('path: "/collector/tasks?tab=executors"');
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
});
