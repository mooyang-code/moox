import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

describe('metadata catalog pages', () => {
  it('keeps source and subject titles while placing search before the actions', () => {
    for (const file of ['sources/index.vue', 'subjects/index.vue']) {
      const source = fs.readFileSync(path.resolve(__dirname, file), 'utf8');
      const toolbar = source.slice(source.indexOf('<div class="page-head">'), source.indexOf('</div>', source.indexOf('<div class="page-head">')) + 6);

      expect(toolbar).toContain('<h2>');
      expect(toolbar).toContain('<a-input-search');
      expect(toolbar).toContain('新增');
      expect(toolbar.indexOf('<a-input-search')).toBeLessThan(toolbar.indexOf('新增'));
      expect(source).toContain('@search="onSearch"');
      expect(source).toContain('@clear="onSearch"');
      expect(source).toContain('keyword: searchKeyword.value.trim() || undefined');
    }
  });

  it('shows the field title in the search toolbar', () => {
    const source = fs.readFileSync(path.resolve(__dirname, 'fields/index.vue'), 'utf8');
    const toolbarStart = source.indexOf('<div class="toolbar">');
    const workbenchStart = source.indexOf('<section class="field-workbench">');
    const toolbar = source.slice(toolbarStart, workbenchStart);

    expect(toolbar).toContain('<h2 class="page-title">字段管理</h2>');
    expect(toolbar.indexOf('字段管理')).toBeLessThan(toolbar.indexOf('<a-input-search'));
    expect(source).toMatch(/\.toolbar-main\s*\{[^}]*display:\s*flex;/);
  });
});
