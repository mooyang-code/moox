import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const normalizeSource = (source: string) => source.replace(/\s+/g, "").replace(/'/g, '"');

describe("collector task management workbench", () => {
  it("keeps the four tabs in tasks, results, status, backfills order", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    const positions = ["采集任务", "采集结果", "运行状态", "补采"].map(label => normalized.indexOf(`label:"${label}"`));

    expect(source).toContain("PageTitleTabs");
    expect(source).toContain('aria-label="采集运行"');
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
    expect(normalized).not.toContain('label:"执行器"');
    expect(normalized).not.toContain("function-instance");
    expect(normalized).toContain("status:RuntimeStatus");
    expect(normalized).toContain("backfills:BackfillJobs");
    expect(normalized).toContain('typeCollectorTaskTab="tasks"|"status"|"backfills"|"results"');
  });

  it("keeps the create action in the task search row", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "../collection-tasks/collection-tasks.vue"), "utf8");
    const firstToolbarEnd = source.indexOf("</a-space>");
    const tableStart = source.indexOf("<a-table");
    const createPosition = source.indexOf("新建采集任务");
    const searchPosition = source.indexOf('placeholder="搜索任务名称 / 任务 ID"');

    expect(createPosition).toBeGreaterThan(0);
    expect(createPosition).toBeLessThan(searchPosition);
    // 下拉即时生效、文本框回车生效：工具栏不再有“查询”按钮
    expect(source.slice(0, firstToolbarEnd)).not.toContain("查询");
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
    expect(normalized).toContain(
      "constactiveTab=computed<CollectorTaskTab>(()=>landingTab.value??normalizeTab(route.query.tab))"
    );
    expect(normalized).not.toMatch(/activeTab\.value=(?!=)/);
  });
});
