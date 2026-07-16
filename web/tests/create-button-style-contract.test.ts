import { readdirSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { parse } from 'vue/compiler-sfc';
import { describe, expect, it } from 'vitest';

const srcRoot = resolve(__dirname, '../src');
const createWords = /新增|新建|添加|创建/;

function vueFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return vueFiles(path);
    return entry.name.endsWith('.vue') ? [path] : [];
  });
}

function walk(node: any, visit: (node: any) => void) {
  visit(node);
  for (const child of node.children || []) walk(child, visit);
  for (const branch of node.branches || []) walk(branch, visit);
}

describe('create button style', () => {
  it('uses a green primary style for every create action', () => {
    const violations: string[] = [];

    for (const file of vueFiles(srcRoot)) {
      const source = readFileSync(file, 'utf8');
      const { descriptor } = parse(source, { filename: file });
      const ast = descriptor.template?.ast;
      if (!ast) continue;

      walk(ast, (node) => {
        if (node.type !== 1 || node.tag !== 'a-button') return;
        const visibleContent = (node.children || []).map((child: any) => child.loc.source).join(' ');
        if (!createWords.test(visibleContent)) return;
        const attributes = Object.fromEntries(
          node.props
            .filter((prop: any) => prop.type === 6)
            .map((prop: any) => [prop.name, prop.value?.content ?? '']),
        );
        if (attributes.type !== 'primary' || attributes.status !== 'success') {
          violations.push(`${file.replace(`${srcRoot}/`, '')}:${node.loc.start.line}`);
        }
      });
    }

    expect(violations, `create buttons without type="primary" status="success":\n${violations.join('\n')}`).toEqual([]);
  });

  it('keeps compact header space launchers as blue icon buttons', () => {
    const header = readFileSync(resolve(srcRoot, 'layout/components/Header/components/header-left/index.vue'), 'utf8');
    const layoutHead = readFileSync(resolve(srcRoot, 'layout/layout-head/index.vue'), 'utf8');

    expect(header).toContain('<a-button type="text" size="small" title="新建空间"');
    expect(layoutHead).toContain('class="space-setting-button" type="text" size="small" title="新建空间"');
  });
});
