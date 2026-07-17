import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const source = fs.readFileSync(
  path.resolve(__dirname, '../src/views/ops/metric-monitor/index.vue'),
  'utf8',
);
const chartSource = fs.readFileSync(
  path.resolve(__dirname, '../src/views/ops/metric-monitor/metric-chart.vue'),
  'utf8',
);
const editorSource = fs.readFileSync(
  path.resolve(__dirname, '../src/views/ops/metric-monitor/metric-rule-editor.vue'),
  'utf8',
);

describe('metric monitor layout', () => {
  it('uses the data-management tab treatment and compact action order', () => {
    expect(source).toContain('<a-tabs v-model:active-key="activeTab" type="rounded" size="small" class="metric-subtabs">');
    expect(source).toContain('<a-tab-pane key="explorer" title="指标看板" />');
    expect(source).toContain('<a-tab-pane key="rules" title="告警规则" />');
    expect(source).not.toContain('PageTitleTabs');
    expect(source).not.toContain('新建规则');

    const actionIndex = source.indexOf('新建指标');
    const filterIndex = source.indexOf('class="filter-band"');
    const serviceFilterIndex = source.indexOf('placeholder="服务"');
    expect(actionIndex).toBeGreaterThan(filterIndex);
    expect(actionIndex).toBeLessThan(serviceFilterIndex);
  });

  it('uses popup errors instead of inline error rows and keeps shared spacing', () => {
    expect(source).not.toContain('errorMessage');
    expect(source).toContain("if (!/metrics catalog is unavailable/i.test(message)) Message.error(message);");
    expect(source.replace(/'/g, '"')).toContain('Message.error("部分指标最新值加载失败，已保留可查询数据。")');
    expect(source).not.toContain('partialState');
    expect(source).not.toContain('chartPartial');
    expect(chartSource).not.toContain('chart-error');
    expect(editorSource).not.toContain('<a-alert v-if="previewError"');
    expect(source).toMatch(/\.page-head\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-2\);/);
    expect(source).toMatch(/\.metric-subtabs\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-3\);/);
    expect(source).toMatch(/\.metric-subtabs\s*:deep\(\.arco-tabs-tab-active\)\s*\{[\s\S]*?background-color:\s*var\(--color-fill-2\);/);
    expect(source).toMatch(/\.filter-band\s*\{[\s\S]*?gap:\s*var\(--moox-space-2\);[\s\S]*?margin-bottom:\s*var\(--moox-space-2\);/);
  });
});
