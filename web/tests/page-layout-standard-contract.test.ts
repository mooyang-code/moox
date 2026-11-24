import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const viewsRoot = path.resolve(__dirname, "../src/views");
const read = (relativePath: string) => {
  const filePath = path.join(viewsRoot, relativePath);
  const source = fs.readFileSync(filePath, "utf8");
  const externalStyles = [...source.matchAll(/<style\b[^>]*\bsrc=["']([^"']+)["'][^>]*>/g)]
    .map(match => fs.readFileSync(path.resolve(path.dirname(filePath), match[1]), "utf8"))
    .join("\n");
  return externalStyles ? `${source}\n${externalStyles}` : source;
};
const readStyle = (relativePath: string) => fs.readFileSync(path.resolve(__dirname, "../src/style", relativePath), "utf8");

const expectMargin = (source: string, selector: string, property: "margin-top" | "margin-bottom", value: number) => {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const token = {
    5: "--moox-space-toolbar-table",
    8: "--moox-space-2",
    12: "--moox-space-3",
    16: "--moox-space-4",
    20: "--moox-space-5"
  }[value];
  const expected = token ? `(?:${value}px|var\\(${token}\\))` : `${value}px`;
  expect(source).toMatch(new RegExp(`${escaped}\\s*\\{[\\s\\S]*?${property}:\\s*${expected};`));
};

describe("page layout standards", () => {
  it("uses the compact single-tab header standard", () => {
    const spaces = read("settings/spaces/index.vue");
    const secrets = read("settings/secrets/index.vue");
    const accounts = read("trading/accounts/index.vue");
    const accountDetail = read("trading/accounts/detail/index.vue");

    expectMargin(spaces, ".page-head", "margin-bottom", 8);
    expect(spaces).toMatch(/\.page-head h2\s*\{[\s\S]*?margin:\s*0;/);
    // 交易账户：三个环境各一个菜单，页面标题是菜单名，不再用标题页签切环境；详情是整页，页签用 ?tab=
    expect(accounts).toContain('<h2 class="page-title">{{ environmentMenus[env].title }}</h2>');
    expect(accounts).not.toContain("PageTitleTabs");
    expect(accounts).toContain('class="moox-page tx-accounts"');
    expect(accounts).toContain("新建账户");
    expect(accounts).toContain('title="组合账户"');
    expect(accounts).toContain('title="交易所"');
    expect(accounts).toContain('title="关联策略"');
    expect(accounts).not.toContain("交易所 · 市场");
    expect(accounts).not.toContain("<a-drawer");
    expect(accountDetail).toContain('class="moox-page tx-detail"');
    expect(accountDetail).not.toContain("<a-drawer");

    expect(secrets).not.toContain('class="filter-bar"');
    expect(secrets.indexOf('placeholder="搜索名称或描述"')).toBeLessThan(secrets.indexOf('placeholder="分类"'));
    expect(secrets.indexOf('placeholder="分类"')).toBeLessThan(secrets.indexOf('placeholder="状态"'));
    expect(secrets.indexOf('placeholder="状态"')).toBeLessThan(secrets.indexOf("新建密钥"));
    expectMargin(secrets, ".page-head", "margin-bottom", 5);
  });

  it("keeps the compute task host and its tabs in one layout", () => {
    const host = read("factor/task-management/index.vue");
    const computeTasks = read("factor/compute-tasks/index.vue");
    const definitions = read("factor/definitions/index.vue");

    expect(host).toContain("PageTitleTabs");
    expect(host).toContain("<keep-alive>");
    expect(computeTasks).toContain("新建计算任务");
    expect(definitions).toContain("新建因子");
    expect(definitions).not.toContain('class="filters"');
  });

  it("keeps the strategy run host, its tabs and the results tab aligned with the factor workbench", () => {
    const host = read("strategy/workbench/index.vue");
    const results = read("strategy/results/index.vue");
    const running = read("strategy/running/index.vue");

    expect(host).toContain("PageTitleTabs");
    expect(host).toContain('label: "运行实例"');
    expect(host).toContain('label: "运行结果"');
    expect(host).toContain('label: "策略回放"');
    expect(results).toContain('type="rounded"');
    expect(results).toContain("InstanceResults");
    expect(running).toContain("新建实例");
    expect(running).not.toContain('class="filters"');
  });

  // 因子定义页没有标签页，按字段管理的工作台布局用提示条说明规则，不在此列。
  it("keeps factor tab explanations in info tips instead of rows under the tabs", () => {
    for (const file of ["factor/compute-tasks/index.vue", "factor/recalc/index.vue"]) {
      const source = read(file);
      expect(source).toContain("InfoTip");
      expect(source).not.toContain('class="tab-hint"');
    }
    expect(read("factor/task-management/index.vue")).not.toContain("<a-alert");
  });

  it("uses the multi-tab spacing standard", () => {
    const subjects = read("data/subjects/index.vue");
    const taskManagement = read("collector/task-management/index.vue");
    const runtimeStatus = read("collector/runtime-status/runtime-status.vue");
    const collectorTasks = read("collector/collection-tasks/collection-tasks.vue");
    const taskResults = read("collector/task-results/index.vue");
    const deployments = read("ops/deployments/index.vue");

    expectMargin(subjects, ".subjects-content", "margin-top", 12);
    expectMargin(taskManagement, ".task-management-content", "margin-top", 12);
    expectMargin(runtimeStatus, ".block-header", "margin-bottom", 8);
    expect(collectorTasks).toContain('class="task-toolbar"');
    expectMargin(collectorTasks, ".task-toolbar", "margin-bottom", 5);
    expect(taskResults).toContain('class="moox-page task-results-page"');
    expect(taskResults).toContain('class="moox-inner"');

    expectMargin(deployments, ".deployments-content", "margin-top", 12);
    expectMargin(deployments, ".filters", "margin-bottom", 8);
  });

  it("normalizes special list workbenches", () => {
    const factorResults = read("factor/results/index.vue");

    expect(factorResults).toContain('class="result-toolbar"');
    expect(factorResults).toContain("#status-extra");
    expect(factorResults).not.toContain("status-strip");
    expect(factorResults).not.toContain("ViewDefinitions");
  });

  it("keeps dashboard page boundaries on the compact spacing rhythm", () => {
    const theme = readStyle("var/global-theme.scss");
    const globalStyle = readStyle("index.scss");
    const healthMonitor = read("ops/monitor/index.vue");
    const strategyDefinitions = read("strategy/definitions/index.vue");
    const hostMonitor = read("ops/host-workbench/host-monitor.vue");
    const packageManage = read("collector/cloud-function/function-package-manage.vue");

    expect(theme).toMatch(/\$space-2:\s*8px;/);
    expect(theme).toMatch(/\$space-4:\s*16px;/);
    expect(globalStyle).toContain("--moox-space-2: #{$space-2};");
    expect(globalStyle).toContain("--moox-space-4: #{$space-4};");
    expect(healthMonitor).toContain("监控告警");
    expect(healthMonitor).toMatch(/\.health-section\s*\{[\s\S]*?margin-top:\s*24px;/);

    expectMargin(strategyDefinitions, ".page-head", "margin-bottom", 8);
    expectMargin(strategyDefinitions, ".top-alert", "margin-bottom", 8);
    expect(hostMonitor).toMatch(/\.host-monitor-page\s*\{[\s\S]*?padding:\s*0 0 var\(--moox-space-5\);/);
    expectMargin(hostMonitor, ".monitor-toolbar", "margin-bottom", 8);
    expectMargin(hostMonitor, ".monitor-summary", "margin-bottom", 8);
    expectMargin(hostMonitor, ".monitor-alert", "margin-bottom", 8);
    expectMargin(packageManage, ".package-toolbar", "margin-bottom", 8);
  });
});
