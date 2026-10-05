import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
const normalizeSource = (value: string) => value.replace(/\s+/g, "").replace(/'/g, '"');
const normalized = normalizeSource(source);

describe("compute task host", () => {
  it("uses PageTitleTabs with keep-alive", () => {
    expect(source).toContain("<PageTitleTabs");
    expect(source).toContain('aria-label="计算任务"');
    expect(source).toContain("<keep-alive>");
    expect(source).toContain("moox-page");
    expect(source).toContain("moox-inner");
  });

  it("orders the tabs 计算任务 -> 计算结果 -> 补算 with no explanation row under them", () => {
    const positions = ["计算任务", "计算结果", "补算"].map(label => source.indexOf(`label: "${label}"`));
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions.every((position, index) => index === 0 || position > positions[index - 1])).toBe(true);
    expect(source).not.toContain("tab-hint");
    expect(source).not.toContain("<a-alert");
  });

  it("normalizes ?tab=, keeps ?set= when switching tabs and drops detail/job", () => {
    expect(normalized).toContain("route.query.tab");
    expect(normalized).toContain('value==="results"||value==="recalc"?value:"tasks"');
    expect(normalized).toContain("router.replace");
    expect(normalized).toContain('path:"/factor/tasks"');
    expect(normalized).toContain("buildTabQuery(route.query,tab)");
  });
});
