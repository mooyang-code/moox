import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const viewsRoot = path.resolve(__dirname, '../src/views');
const read = (relativePath: string) => fs.readFileSync(path.join(viewsRoot, relativePath), 'utf8');

const expectMargin = (source: string, selector: string, property: 'margin-top' | 'margin-bottom', value: number) => {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  expect(source).toMatch(new RegExp(`${escaped}\\s*\\{[\\s\\S]*?${property}:\\s*${value}px;`));
};

describe('page layout standards', () => {
  it('uses the compact single-tab header standard', () => {
    const subjects = read('data/subjects/index.vue');
    const spaces = read('settings/spaces/index.vue');
    const secrets = read('settings/secrets/index.vue');
    const accounts = read('trading/account-overview/account-overview.vue');

    expectMargin(subjects, '.page-head', 'margin-bottom', 8);
    expectMargin(spaces, '.page-head', 'margin-bottom', 8);
    expect(spaces).toMatch(/\.page-head h2\s*\{[\s\S]*?margin:\s*0;/);
    expectMargin(accounts, '.page-head', 'margin-bottom', 8);

    expect(secrets).not.toContain('class="filter-bar"');
    expect(secrets.indexOf('placeholder="搜索名称或描述"')).toBeLessThan(secrets.indexOf('placeholder="分类"'));
    expect(secrets.indexOf('placeholder="分类"')).toBeLessThan(secrets.indexOf('placeholder="状态"'));
    expect(secrets.indexOf('placeholder="状态"')).toBeLessThan(secrets.indexOf('新增秘钥'));
    expectMargin(secrets, '.page-head', 'margin-bottom', 8);
  });

  it('keeps factor list controls in one compact header', () => {
    const definitions = read('factor/definitions/index.vue');
    const bindings = read('factor/bindings/index.vue');

    expect(definitions).not.toContain('维护生产计算用的 Python 因子源码');
    expect(definitions).not.toContain('本页管理的是 factor 服务自己的计算定义');
    expect(definitions).not.toContain('class="filters"');
    expectMargin(definitions, '.page-head', 'margin-bottom', 8);

    expect(bindings).not.toContain('把启用的因子绑定到 K 线数据集');
    expect(bindings).not.toContain('class="filters"');
    expectMargin(bindings, '.page-head', 'margin-bottom', 8);
  });

  it('uses the multi-tab spacing standard', () => {
    const taskManagement = read('collector/task-management/index.vue');
    const taskInstances = read('collector/task-instances/task-instances.vue');
    const collectorRules = read('collector/collector-rules/collector-rules.vue');
    const dataManagement = read('collector/data-management/index.vue');
    const gatewayNodes = read('ops/service-management/gateway-nodes.vue');
    const serviceInstances = read('settings/service-deployments/index.vue');
    const storage = read('ops/storage/index.vue');
    const storageNodes = read('ops/storage/nodes.vue');
    const storageRoutes = read('ops/storage/routes.vue');
    const storageArchive = read('ops/storage/archive.vue');
    const tradeRecords = read('trading/trade-record/trade-record.vue');

    expectMargin(taskManagement, '.task-management-content', 'margin-top', 12);
    expectMargin(taskInstances, '.task-toolbar', 'margin-bottom', 8);
    expect(collectorRules).toContain('class="rule-toolbar"');
    expectMargin(collectorRules, '.rule-toolbar', 'margin-bottom', 8);
    expect(dataManagement).toMatch(/:deep\(\.page-head\)\s*\{[\s\S]*?margin-bottom:\s*8px;/);

    expectMargin(gatewayNodes, '.toolbar', 'margin-bottom', 8);
    expect(serviceInstances).not.toContain('class="page-head"');
    expect(serviceInstances.indexOf('新增实例')).toBeLessThan(serviceInstances.indexOf('placeholder="网关节点"'));
    expectMargin(serviceInstances, '.filters', 'margin-bottom', 8);

    expectMargin(storage, '.storage-config-content', 'margin-top', 12);
    for (const source of [storageNodes, storageRoutes, storageArchive]) expectMargin(source, '.page-actions', 'margin-bottom', 8);
    expectMargin(tradeRecords, '.record-view-tabs', 'margin-bottom', 12);
    expectMargin(tradeRecords, '.filter-bar', 'margin-bottom', 8);
  });

  it('normalizes special list workbenches', () => {
    const cloudNodes = read('collector/cloud-node/cloud-node.vue');
    const factorResults = read('factor/results/index.vue');
    const positions = read('trading/position-detail/position-detail.vue');
    const viewDefinitions = read('data/views/index.vue');
    const datasetDefinitions = read('data/datasets/index.vue');
    const datasetBrowse = read('data/browse/index.vue');
    const viewBrowse = read('data/view-browse/index.vue');

    expect(cloudNodes).toContain('<a-space class="cloud-node-action-bar" wrap>');
    expect(cloudNodes).toContain('<a-space class="cloud-node-filter-bar" wrap>');
    expect(cloudNodes).not.toContain('class="cloud-node-toolbar"');
    expect(cloudNodes).not.toContain('.moox-inner .a-row');
    const cloudNodeLayoutIndexes = [
      '<h2>云节点</h2>',
      'class="cloud-node-action-bar"',
      '<span>批量新增</span>',
      '<span>批量部署</span>',
      '<span>批量删除</span>',
      '<span>云账户管理</span>',
      '<span>代码包版本</span>',
      'class="cloud-node-filter-bar"',
      'placeholder="请选择云账户"',
      'placeholder="请输入节点ID"',
      'placeholder="地区"',
      'placeholder="节点类型"',
      'placeholder="节点状态"',
      '<a-button type="primary" @click="search">',
      '<icon-search />',
      '<span>查询</span>',
      '<a-table',
    ].map(marker => cloudNodes.indexOf(marker));
    for (const index of cloudNodeLayoutIndexes) {
      expect(index).toBeGreaterThanOrEqual(0);
    }
    for (let index = 1; index < cloudNodeLayoutIndexes.length; index += 1) {
      expect(cloudNodeLayoutIndexes[index - 1]).toBeLessThan(cloudNodeLayoutIndexes[index]);
    }
    expectMargin(cloudNodes, '.page-head', 'margin-bottom', 8);
    for (const toolbar of ['.cloud-node-action-bar', '.cloud-node-filter-bar']) {
      const escapedToolbar = toolbar.replace('.', '\\.');
      expect(cloudNodes).toMatch(new RegExp(
        `${escapedToolbar}\\s*\\{[^}]*display:\\s*flex;[^}]*width:\\s*100%;[^}]*max-width:\\s*100%;[^}]*min-width:\\s*0;[^}]*justify-content:\\s*flex-start;[^}]*margin-bottom:\\s*8px;`,
      ));
    }
    expect(cloudNodes).toMatch(/\.moox-page\s*\{[^}]*contain:\s*inline-size;[^}]*overflow-x:\s*hidden;[^}]*overflow-y:\s*auto;/);
    expect(cloudNodes).toMatch(/\.moox-page :deep\(\.arco-spin\),\s*\.moox-page :deep\(\.arco-spin-children\)\s*\{[^}]*overflow-x:\s*hidden;/);
    expect(cloudNodes).toMatch(/\.moox-inner\s*\{[^}]*overflow-x:\s*hidden;/);
    expect(cloudNodes).not.toContain('width: calc(100% - 72px)');
    expect(cloudNodes).not.toMatch(/\.cloud-node-toolbar\s*\{[^}]*flex:\s*1;/);
    expect(cloudNodes).not.toMatch(/\.cloud-node-toolbar\s*\{[^}]*justify-content:\s*flex-end;/);
    expect(cloudNodes).not.toMatch(/@media\s*\(max-width:\s*768px\)\s*\{[\s\S]*?\.page-head\s*\{[^}]*flex-direction:\s*column;/);

    expect(factorResults).toContain('class="factor-results-content"');
    expectMargin(factorResults, '.factor-results-content', 'margin-top', 12);
    expect(factorResults).toContain(':embedded="true"');
    expect(factorResults).toMatch(/\.factor-results-workbench > \.moox-inner\s*\{[\s\S]*?display:\s*flex;[\s\S]*?flex-direction:\s*column;/);
    expect(factorResults).toMatch(/\.factor-results-content :deep\(\.moox-page\)\s*\{[\s\S]*?overflow:\s*auto;/);

    expect(positions).toContain('<h2>持仓详情</h2>');
    expect(positions).not.toContain('<a-button @click="loadPositions">');
    expectMargin(positions, '.position-toolbar', 'margin-bottom', 8);

    for (const source of [viewDefinitions, datasetDefinitions, datasetBrowse, viewBrowse]) {
      expectMargin(source, '.page-head', 'margin-bottom', 8);
    }
  });
});
