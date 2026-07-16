import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const root = resolve(__dirname, '..');
const pages = {
  nodes: readFileSync(resolve(root, 'src/views/ops/storage/nodes.vue'), 'utf8'),
  routes: readFileSync(resolve(root, 'src/views/ops/storage/routes.vue'), 'utf8'),
  archive: readFileSync(resolve(root, 'src/views/ops/storage/archive.vue'), 'utf8'),
};

function pageActions(page: string, source: string): string {
  const match = source.match(/<div class="page-actions">([\s\S]*?)<\/div>/);
  if (!match) throw new Error(`${page} is missing a page-actions block`);
  return match[1];
}

describe('storage page actions contract', () => {
  it.each(Object.entries(pages))('%s renders actions inline below the page title', (_page, source) => {
    expect(source).toContain('<div class="page-actions">');
    expect(source).not.toContain('PageTabActions');
  });

  it('keeps the remaining storage controls and removes refresh actions', () => {
    expect(pageActions('nodes', pages.nodes)).toContain('新增节点');
    expect(pageActions('routes', pages.routes)).toContain('新增路由');
    expect(pageActions('archive', pages.archive)).toMatch(/datasetFilter[\s\S]*icon-search[\s\S]*查询/);
    expect(Object.values(pages).join('\n')).not.toContain('icon-refresh');
    expect(Object.values(pages).join('\n')).not.toContain('刷新节点列表');
    expect(Object.values(pages).join('\n')).not.toContain('刷新路由列表');
    expect(Object.values(pages).join('\n')).not.toContain('刷新归档列表');
  });

  it.each(Object.entries(pages))('%s aligns its action row to the left', (_page, source) => {
    expect(source).toMatch(/\.page-actions\s*\{[\s\S]*?justify-content:\s*flex-start;/);
  });

  it('removes the storage topology warning from the node page', () => {
    expect(pages.nodes).not.toContain('<a-alert');
    expect(pages.nodes).not.toContain('topology-alert');
  });

  it('keeps only the search input and query button in archive actions', () => {
    const actions = pageActions('archive', pages.archive);
    expect(actions.match(/<a-input\b/g)).toHaveLength(1);
    expect(actions.match(/<a-button\b/g)).toHaveLength(1);
    expect(actions).not.toContain('<a-switch');
    expect(pages.archive).not.toContain('debugMode');
    expect(pages.archive).not.toContain('technicalDetails');
  });

  it('uses green create actions while keeping the archive query action primary', () => {
    expect(pageActions('nodes', pages.nodes)).toMatch(/<a-button\s+type="primary"\s+status="success"[^>]*>[\s\S]*新增节点/);
    expect(pageActions('routes', pages.routes)).toMatch(/<a-button\s+type="primary"\s+status="success"[^>]*>[\s\S]*新增路由/);
    expect(pageActions('archive', pages.archive)).toMatch(/<a-button\s+type="primary"[^>]*>[\s\S]*查询/);
    expect(pageActions('archive', pages.archive)).not.toContain('status="success"');
  });
});
