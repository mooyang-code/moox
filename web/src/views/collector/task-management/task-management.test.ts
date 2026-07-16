import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

describe('collector task management workbench', () => {
  it('combines collection rules and task instances in one ordered tab surface', () => {
    const source = fs.readFileSync(path.resolve(__dirname, 'index.vue'), 'utf8');
    const positions = ['采集规则', '任务实例'].map((label) => source.indexOf(`label: '${label}'`));

    expect(source).toContain('PageTitleTabs');
    expect(source).toContain('aria-label="采集任务"');
    expect(positions.every((position) => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
    expect(source).toContain("type CollectorTaskTab = 'rules' | 'instances'");
  });

  it('keeps one visible menu and redirects the old task URL to its tab', () => {
    const menu = fs.readFileSync(path.resolve(__dirname, '../../../api/modules/system/static-menu.ts'), 'utf8');
    const routes = fs.readFileSync(path.resolve(__dirname, '../../../router/route.ts'), 'utf8');

    expect(menu).toContain('menu("0303", "03", "/collector/rules", "collector-rules"');
    expect(menu).not.toContain('menu("0304"');
    expect(routes).toContain('component: () => import("@/views/collector/task-management/index.vue")');
    expect(routes).toContain('redirect: { path: "/collector/rules", query: { tab: "instances" } }');
  });

  it('removes the standalone package page and keeps package management on cloud nodes', () => {
    const menu = fs.readFileSync(path.resolve(__dirname, '../../../api/modules/system/static-menu.ts'), 'utf8');
    const routes = fs.readFileSync(path.resolve(__dirname, '../../../router/route.ts'), 'utf8');
    const cloudNodes = fs.readFileSync(path.resolve(__dirname, '../cloud-node/cloud-node.vue'), 'utf8');

    expect(menu).not.toContain('menu("0302"');
    expect(routes).toContain('path: "/collector/packages"');
    expect(routes).toContain('redirect: "/collector/cloudnodes"');
    expect(routes).not.toContain('component: () => import("@/views/collector/cloud-node/function-package-manage.vue")');
    expect(cloudNodes).toContain("import FunctionPackageManage from './function-package-manage.vue'");
  });

  it('places the create action in the collection rule search row', () => {
    const rules = fs.readFileSync(path.resolve(__dirname, '../collector-rules/collector-rules.vue'), 'utf8');
    const firstToolbarEnd = rules.indexOf('</a-space>');
    const tableStart = rules.indexOf('<a-table');

    expect(rules.indexOf('<span>新建任务</span>')).toBeGreaterThan(0);
    expect(rules.indexOf('<span>新建任务</span>')).toBeLessThan(firstToolbarEnd);
    expect(rules.slice(firstToolbarEnd, tableStart)).not.toContain('新建任务');
  });
});
