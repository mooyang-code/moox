import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const root = resolve(__dirname, '..');
const pages = {
  nodes: readFileSync(resolve(root, 'src/views/ops/storage/nodes.vue'), 'utf8'),
  routes: readFileSync(resolve(root, 'src/views/ops/storage/routes.vue'), 'utf8'),
  archive: readFileSync(resolve(root, 'src/views/ops/storage/archive.vue'), 'utf8'),
};

function pageTabActions(page: string, source: string): string {
  const match = source.match(/<PageTabActions>([\s\S]*?)<\/PageTabActions>/);
  if (!match) throw new Error(`${page} is missing a PageTabActions block`);
  return match[1];
}

describe('storage page tab actions contract', () => {
  it.each(Object.entries(pages))('%s uses the shared page tab actions container', (_page, source) => {
    expect(source).toContain("import PageTabActions from '@/components/page-tab-actions/index.vue'");
    expect(source).toContain('<PageTabActions>');
    expect(source).not.toContain('class="page-toolbar"');
  });

  it('keeps each storage page action set in its original order', () => {
    expect(pageTabActions('nodes', pages.nodes)).toMatch(/刷新节点列表[\s\S]*新增节点/);
    expect(pageTabActions('routes', pages.routes)).toMatch(/刷新路由列表[\s\S]*新增路由/);
    expect(pageTabActions('archive', pages.archive)).toMatch(/datasetFilter[\s\S]*debugMode[\s\S]*刷新归档列表/);
  });
});
