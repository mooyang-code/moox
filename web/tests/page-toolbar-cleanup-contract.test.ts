import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const viewsRoot = path.resolve(__dirname, "../src/views");
const read = (relativePath: string) => fs.readFileSync(path.join(viewsRoot, relativePath), "utf8");

describe("page toolbar cleanup contract", () => {
  it("keeps the five requested pages compact and action focused", () => {
    const hostMonitor = read("ops/host-workbench/host-monitor.vue");
    expect(hostMonitor).toContain("<strong>资源状态</strong>");
    expect(hostMonitor).not.toContain("主机资源状态");
    expect(hostMonitor).not.toContain("lastRefreshAt");
    expect(hostMonitor).not.toContain("formatAge");

    const services = read("settings/service-deployments/index.vue");
    expect(services).not.toContain('class="top-alert"');
    expect(services).not.toContain("<icon-refresh />");
    expect(services).toMatch(/\.filters\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-2\);/);

    const serviceManagement = read("ops/service-management/index.vue");
    expect(serviceManagement).toMatch(/\.management-content\s*\{[\s\S]*?margin-top:\s*var\(--moox-space-3\);/);

    const secrets = read("settings/secrets/index.vue");
    expect(secrets).not.toContain("统一管理 admin 本地秘钥");
    expect(secrets).not.toContain("<icon-refresh />");
    expect(secrets.indexOf("<h2>秘钥管理</h2>")).toBeLessThan(secrets.indexOf('placeholder="搜索名称或描述"'));

    const fields = read("data/fields/index.vue");
    expect(fields).not.toContain('class="field-total"');
    expect(fields).not.toContain('content="刷新"');
    expect(fields).toContain("<template #icon><icon-search /></template>查询");

    const sources = read("data/sources/index.vue");
    expect(sources).not.toContain("<icon-refresh />");
    expect(sources).toMatch(/\.page-head\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-2\);/);
  });

  it("removes refresh and reset controls reviewed beside create or query actions", () => {
    const forbiddenToolbarMarkers: Record<string, string[]> = {
      "collector/cloud-node/cloud-node.vue": ['@click="reset"'],
      "collector/cloud-node/function-package-manage.vue": ['@click="resetSearch"'],
      "collector/collector-rules/collector-rules.vue": ['@click="reset"'],
      "collector/task-instances/task-instances.vue": ['@click="reset"'],
      "container/ssh-hosts/ssh-hosts.vue": ['@click="reset"'],
      "data/datasets/index.vue": ['<a-button :disabled="!selectedSpaceId" @click="load">'],
      "data/datasets/components/dataset-column-panel.vue": ['<a-button :disabled="!datasetId" @click="load">'],
      "data/datasets/components/dataset-subject-panel.vue": ['<a-button :disabled="!datasetId" @click="load">'],
      "data/subjects/index.vue": ['<a-button :disabled="!selectedSpaceId" @click="load">', '<a-button @click="loadSymbols">'],
      "data/views/index.vue": ['<a-button :disabled="!selectedSpaceId" @click="load">'],
      "data/views/components/view-column-panel.vue": ['<a-button :disabled="!viewId" @click="load">'],
      "factor/bindings/index.vue": ['<a-button :disabled="!selectedSpaceId" @click="load">'],
      "factor/definitions/index.vue": ['<a-button @click="load">'],
      "ops/service-management/gateway-nodes.vue": ['aria-label="刷新节点状态"'],
      "settings/secrets/index.vue": ['<a-button @click="load">'],
      "settings/service-deployments/index.vue": ['<a-button @click="load">'],
      "settings/spaces/index.vue": ['<a-button @click="load">'],
      "trading/account-overview/account-overview.vue": ['<a-button @click="loadAccounts">'],
      "trading/trade-record/trade-record.vue": [
        '<a-button size="small" @click="loadOrders">',
        '<a-button size="small" @click="loadTrades">'
      ]
    };

    for (const [relativePath, markers] of Object.entries(forbiddenToolbarMarkers)) {
      const source = read(relativePath);
      for (const marker of markers) expect(source, `${relativePath}: ${marker}`).not.toContain(marker);
    }
  });

  it("keeps independent operational refresh controls", () => {
    expect(read("ops/host-workbench/host-monitor.vue")).toContain('aria-label="刷新主机监控"');
    expect(read("container/ssh-file-manager/ssh-file-manager.vue")).toContain('@click="refresh"');
  });
});
