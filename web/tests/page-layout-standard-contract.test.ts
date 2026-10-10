import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const viewsRoot = path.resolve(__dirname, "../src/views");
const staticMenuSource = fs.readFileSync(path.resolve(__dirname, "../src/api/modules/system/static-menu.ts"), "utf8");
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

const extractBlock = (source: string, opening: string, closing: string) => {
  const start = source.indexOf(opening);
  if (start < 0) throw new Error(`missing opening marker: ${opening}`);
  const end = source.indexOf(closing, start + opening.length);
  if (end < 0) throw new Error(`missing closing marker after: ${opening}`);
  return source.slice(start, end + closing.length);
};

const expectStrictOrder = (source: string, markers: string[]) => {
  let previous = -1;
  for (const marker of markers) {
    const current = source.indexOf(marker);
    expect(current, `missing or out-of-order marker: ${marker}`).toBeGreaterThan(previous);
    previous = current;
  }
};

describe("page layout standards", () => {
  it("uses the compact single-tab header standard", () => {
    const spaces = read("settings/spaces/index.vue");
    const secrets = read("settings/secrets/index.vue");
    const accounts = read("trading/account-overview/account-overview.vue");
    const accountWorkbench = read("trading/account-workbench/index.vue");

    expectMargin(spaces, ".page-head", "margin-bottom", 8);
    expect(spaces).toMatch(/\.page-head h2\s*\{[\s\S]*?margin:\s*0;/);
    expectMargin(accounts, ".page-head", "margin-bottom", 16);
    expect(accounts).toContain("创建账户");
    expect(accounts).toContain('title="操作"');
    expect(accounts).toContain('title="最近同步"');
    expect(accounts).toContain('title="账户类型"');
    expect(accounts).toContain('title="运行环境"');
    expect(accounts).not.toContain("Readiness");
    expect(accounts).not.toContain("创建 Paper 模拟");
    expect(accounts).not.toContain("创建 Live 账户");
    expect(accounts).not.toContain("<h2>执行账户</h2>");
    expect(accounts).not.toContain("PageTitleTabs");
    expect(accountWorkbench).toContain('label: "执行账户"');
    expect(accountWorkbench).toContain('label: "组合账户"');
    expect(accountWorkbench).toContain('class="trading-account-content"');
    expect(accountWorkbench).toContain(':embedded="true"');
    expect(staticMenuSource.match(/\/trading\/accounts/g)?.length).toBe(2);
    expect(staticMenuSource).not.toContain('"0504"');
    expect(staticMenuSource).not.toContain("/trading/logical-accounts");
    const strategyDefinitionsPos = staticMenuSource.indexOf('menu("025002",');
    const strategyRunningPos = staticMenuSource.indexOf('menu("025001",');
    expect(strategyDefinitionsPos).toBeLessThan(strategyRunningPos);

    expect(secrets).not.toContain('class="filter-bar"');
    expect(secrets.indexOf('placeholder="搜索名称或描述"')).toBeLessThan(secrets.indexOf('placeholder="分类"'));
    expect(secrets.indexOf('placeholder="分类"')).toBeLessThan(secrets.indexOf('placeholder="状态"'));
    expect(secrets.indexOf('placeholder="状态"')).toBeLessThan(secrets.indexOf("新增秘钥"));
    expectMargin(secrets, ".page-head", "margin-bottom", 5);
  });

  it("keeps the compute task host and its tabs in one layout", () => {
    const host = read("factor/task-management/index.vue");
    const computeTasks = read("factor/compute-tasks/index.vue");
    const definitions = read("factor/definitions/index.vue");

    expect(host).toContain("PageTitleTabs");
    expect(host).toContain("<keep-alive>");
    expect(computeTasks).toContain("新建计算任务");
    expect(definitions).toContain("新增因子");
    expect(definitions).not.toContain('class="filters"');
  });

  it("keeps factor tab explanations in info tips instead of rows under the tabs", () => {
    for (const file of ["factor/compute-tasks/index.vue", "factor/recalc/index.vue", "factor/definitions/index.vue"]) {
      const source = read(file);
      expect(source).toContain("InfoTip");
      expect(source).not.toContain('class="tab-hint"');
    }
    expect(read("factor/task-management/index.vue")).not.toContain("<a-alert");
  });

  it("uses the multi-tab spacing standard", () => {
    const subjects = read("data/subjects/index.vue");
    const taskManagement = read("collector/task-management/index.vue");
    const taskInstances = read("collector/task-instances/task-instances.vue");
    const collectorTasks = read("collector/collection-tasks/collection-tasks.vue");
    const taskResults = read("collector/task-results/index.vue");
    const deployments = read("ops/deployments/index.vue");
    const storage = read("ops/storage/index.vue");
    const storageNodes = read("ops/storage/nodes.vue");
    const storageArchive = read("ops/storage/archive.vue");
    const tradeRecords = read("trading/trade-record/trade-record.vue");

    expectMargin(subjects, ".subjects-content", "margin-top", 12);
    expectMargin(taskManagement, ".task-management-content", "margin-top", 12);
    expectMargin(taskInstances, ".task-toolbar", "margin-bottom", 8);
    expect(collectorTasks).toContain('class="task-toolbar"');
    expectMargin(collectorTasks, ".task-toolbar", "margin-bottom", 5);
    expect(taskResults).toContain('class="moox-page task-results-page"');
    expect(taskResults).toContain('class="moox-inner"');

    expectMargin(deployments, ".deployments-content", "margin-top", 12);
    expectMargin(deployments, ".filters", "margin-bottom", 8);

    expectMargin(storage, ".storage-config-content", "margin-top", 12);
    expect(storageNodes).toContain(".page-head");
    expectMargin(storageArchive, ".page-actions", "margin-bottom", 8);
    expect(tradeRecords).not.toContain('import PageTitleTabs from "@/components/page-title-tabs/index.vue";');
    expect(tradeRecords).toContain("<h2>交易记录</h2>");
    expect(tradeRecords).toContain('class="orders-workbench-content"');
    expect(tradeRecords).not.toContain("<a-tabs");
    expect(tradeRecords).not.toContain('class="page-title-tabs"');
    expect(tradeRecords).not.toContain('class="orders-account-toolbar"');
    expect(tradeRecords).not.toContain("刷新账户状态");
    expect(tradeRecords).not.toContain("refreshAccounts");
    expect(tradeRecords).not.toContain("仅未完成");
    expect(tradeRecords).not.toContain("onlyOpen");
    expect(tradeRecords.match(/<a-space class="filter-bar" wrap>/g)).toHaveLength(1);
    expect(tradeRecords.match(/placeholder="执行账户"/g)).toHaveLength(1);
    expect(tradeRecords).toContain(">成交明细</h3>");
    expect(tradeRecords).toContain("openOrderDetail");
    expect(tradeRecords).not.toContain("fillTimeRange");
    expectMargin(tradeRecords, ".orders-workbench-content", "margin-top", 12);
    expect(tradeRecords).toMatch(/\.filter-bar\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-tight\);/);
    expect(tradeRecords).toMatch(/\.orders-page\s*:deep\(\.state-select\)\s*\{[\s\S]*?width:\s*140px;/);
    expect(tradeRecords).toMatch(/\.orders-page\s*:deep\(\.time-range\)\s*\{[\s\S]*?width:\s*300px;/);
  });

  it("normalizes special list workbenches", () => {
    const cloudNodes = read("collector/cloud-node/cloud-node.vue");
    const factorResults = read("factor/results/index.vue");
    const positions = read("trading/position-detail/position-detail.vue");
    const viewBrowse = read("data/view-browse/index.vue");

    const cloudNodeActions = extractBlock(cloudNodes, '<a-space class="cloud-node-action-bar" wrap>', "</a-space>");
    const cloudNodeFilters = extractBlock(cloudNodes, '<a-space class="cloud-node-filter-bar" wrap>', "</a-space>");
    expectStrictOrder(cloudNodeActions, [
      '<a-button type="primary" status="success" @click="onBatchAdd" :disabled="batchChangeProcessing">',
      "<span>批量新增</span>",
      '<a-button type="primary" status="warning" @click="batchDeploy" :disabled="batchChangeProcessing">',
      "<span>批量部署</span>",
      '<a-button type="primary" status="danger" @click="batchDelete" :disabled="batchChangeProcessing">',
      "<span>批量删除</span>",
      '<a-button type="outline" @click="onCloudAccountManage">',
      "<span>云账户管理</span>",
      '<a-button type="outline" @click="onFunctionPackageManage">',
      "<span>代码包版本</span>"
    ]);
    expectStrictOrder(cloudNodeFilters, [
      '<a-select v-model="form.cloudAccountId" placeholder="请选择云账户" style="width: 200px" allow-clear>',
      '<a-input v-model="form.nodeId" placeholder="请输入节点ID" allow-clear />',
      '<a-select placeholder="地区" v-model="form.region" style="width: 200px" allow-clear>',
      '<a-select placeholder="节点类型" v-model="form.nodeType" style="width: 180px" allow-clear>',
      '<a-button type="primary" @click="search">',
      "<icon-search />",
      "<span>查询</span>"
    ]);
    expect(cloudNodes).not.toContain('class="cloud-node-toolbar"');
    expect(cloudNodes).not.toContain(".moox-inner .a-row");
    expect(cloudNodes).not.toContain("<h2>云节点</h2>");
    expectStrictOrder(cloudNodes, [cloudNodeActions, cloudNodeFilters, "<a-table"]);
    const toolbarRule = cloudNodes.match(/\.cloud-node-action-bar\s*,\s*\.cloud-node-filter-bar\s*\{([^}]*)\}/);
    expect(toolbarRule).not.toBeNull();
    const toolbarDeclarations = toolbarRule?.[1] || "";
    for (const [property, value] of [
      ["display", "flex"],
      ["width", "100%"],
      ["max-width", "100%"],
      ["min-width", "0"],
      ["justify-content", "flex-start"],
      ["margin-bottom", "var(--moox-space-tight)"]
    ]) {
      expect(toolbarDeclarations).toContain(`${property}: ${value};`);
    }
    expect(cloudNodes).toMatch(/\.moox-page\s*\{[^}]*contain:\s*inline-size;[^}]*overflow-x:\s*hidden;[^}]*overflow-y:\s*auto;/);
    expect(cloudNodes).toMatch(
      /\.moox-page :deep\(\.arco-spin\),\s*\.moox-page :deep\(\.arco-spin-children\)\s*\{[^}]*overflow-x:\s*hidden;/
    );
    expect(cloudNodes).toMatch(/\.moox-inner\s*\{[^}]*overflow-x:\s*hidden;/);
    expect(cloudNodes).not.toContain("width: calc(100% - 72px)");
    expect(cloudNodes).not.toMatch(/\.cloud-node-toolbar\s*\{[^}]*flex:\s*1;/);
    expect(cloudNodes).not.toMatch(/\.cloud-node-toolbar\s*\{[^}]*justify-content:\s*flex-end;/);
    expect(cloudNodes).not.toMatch(/@media\s*\(max-width:\s*768px\)\s*\{[\s\S]*?\.page-head\s*\{[^}]*flex-direction:\s*column;/);

    expect(factorResults).toContain('class="result-toolbar"');
    expect(factorResults).toContain("#status-extra");
    expect(factorResults).not.toContain("status-strip");
    expect(factorResults).toContain(':embedded="true"');
    expect(factorResults).not.toContain("ViewDefinitions");

    expect(positions).toContain("<h2>持仓</h2>");
    expect(positions).not.toContain('<a-button @click="loadPositions">');
    expect(positions).toMatch(/\.position-filter-bar\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-tight\);/);

    expectMargin(viewBrowse, ".page-head", "margin-bottom", 8);
  });

  it("keeps dashboard page boundaries on the compact spacing rhythm", () => {
    const theme = readStyle("var/global-theme.scss");
    const globalStyle = readStyle("index.scss");
    const healthMonitor = read("ops/monitor/index.vue");
    const dataImport = read("data/import/index.vue");
    const strategyOverview = read("strategy/overview/index.vue");
    const hostMonitor = read("ops/host-workbench/host-monitor.vue");
    const packageManage = read("collector/cloud-node/function-package-manage.vue");

    expect(theme).toMatch(/\$space-2:\s*8px;/);
    expect(theme).toMatch(/\$space-4:\s*16px;/);
    expect(globalStyle).toContain("--moox-space-2: #{$space-2};");
    expect(globalStyle).toContain("--moox-space-4: #{$space-4};");
    expect(healthMonitor).toContain("监控告警");
    expect(healthMonitor).toMatch(/\.health-section\s*\{[\s\S]*?margin-top:\s*24px;/);

    expect(dataImport).toMatch(/\.page-head,\s*\.preview-head\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-2\);/);
    expect(dataImport).toMatch(/\.sync-alert\s*\{\s*margin:\s*var\(--moox-space-2\) 0;\s*\}/);
    expectMargin(strategyOverview, ".page-head", "margin-bottom", 8);
    expectMargin(strategyOverview, ".top-alert", "margin-bottom", 8);
    expect(hostMonitor).toMatch(/\.host-monitor-page\s*\{[\s\S]*?padding:\s*0 0 var\(--moox-space-5\);/);
    expectMargin(hostMonitor, ".monitor-toolbar", "margin-bottom", 8);
    expectMargin(hostMonitor, ".monitor-summary", "margin-bottom", 8);
    expectMargin(hostMonitor, ".monitor-alert", "margin-bottom", 8);
    expectMargin(packageManage, ".package-toolbar", "margin-bottom", 8);
  });
});
