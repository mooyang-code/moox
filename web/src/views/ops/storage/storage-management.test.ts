import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

describe('storage configuration workbench', () => {
  it('collects the storage pages into one ordered tab surface', () => {
    const source = fs.readFileSync(path.resolve(__dirname, 'index.vue'), 'utf8');
    const positions = ['主存节点', '主存路由', '归档文件'].map((label) => source.indexOf(`label: '${label}'`));

    expect(source).toContain('PageTitleTabs');
    expect(source).toContain('aria-label="存储配置"');
    expect(positions.every((position) => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
    expect(source).toContain("type StorageConfigTab = 'nodes' | 'routes' | 'archive'");
  });

  it('keeps one visible storage menu and redirects legacy child URLs to tabs', () => {
    const menu = fs.readFileSync(path.resolve(__dirname, '../../../api/modules/system/static-menu.ts'), 'utf8');
    const routes = fs.readFileSync(path.resolve(__dirname, '../../../router/route.ts'), 'utf8');

    expect(menu).toContain('menu("0606", "06", "/ops/storage/nodes", "ops-storage"');
    expect(menu).not.toContain('menu("060601"');
    expect(menu).not.toContain('menu("060602"');
    expect(menu).not.toContain('menu("060603"');
    expect(routes).toContain('component: () => import("@/views/ops/storage/index.vue")');
    expect(routes).toContain('redirect: { path: "/ops/storage/nodes", query: { tab: "routes" } }');
    expect(routes).toContain('redirect: { path: "/ops/storage/nodes", query: { tab: "archive" } }');
  });
});
