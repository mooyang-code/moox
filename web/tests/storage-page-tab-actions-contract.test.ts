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

  it('keeps each storage page action set in its original order', () => {
    expect(pageActions('nodes', pages.nodes)).toMatch(/刷新节点列表[\s\S]*新增节点/);
    expect(pageActions('routes', pages.routes)).toMatch(/刷新路由列表[\s\S]*新增路由/);
    expect(pageActions('archive', pages.archive)).toMatch(/datasetFilter[\s\S]*debugMode[\s\S]*刷新归档列表/);
  });

  it('removes the storage topology warning from the node page', () => {
    expect(pages.nodes).not.toContain('<a-alert');
    expect(pages.nodes).not.toContain('topology-alert');
  });
});
