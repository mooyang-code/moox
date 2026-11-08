import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("metadata catalog pages", () => {
  it("keeps source search and subject tag/member navigation", () => {
    const sources = fs.readFileSync(path.resolve(__dirname, "sources/index.vue"), "utf8");
    const toolbar = sources.slice(
      sources.indexOf('<div class="page-head">'),
      sources.indexOf("</div>", sources.indexOf('<div class="page-head">')) + 6
    );
    expect(toolbar).toContain("<h2>");
    expect(toolbar).toContain("<a-input-search");
    expect(toolbar).toContain("新建");
    expect(toolbar.indexOf("<a-input-search")).toBeLessThan(toolbar.indexOf("新建"));
    expect(sources).toContain('@search="onSearch"');
    expect(sources).toContain('@clear="onSearch"');
    expect(sources).toContain("keyword: searchKeyword.value.trim() || undefined");

    const subjects = fs.readFileSync(path.resolve(__dirname, "subjects/index.vue"), "utf8");
    expect(subjects).not.toContain("<h2>数据对象</h2>");
    expect(subjects).toContain("PageTitleTabs");
    expect(subjects).toContain('label: "标签"');
    expect(subjects).toContain('label: "标签成员"');
  });

  it("shows the field title in the search toolbar", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "fields/index.vue"), "utf8");
    const toolbarStart = source.indexOf('<div class="toolbar">');
    const workbenchStart = source.indexOf('<section class="field-workbench">');
    const toolbar = source.slice(toolbarStart, workbenchStart);

    expect(toolbar).toContain('<h2 class="page-title">字段管理</h2>');
    expect(toolbar.indexOf("字段管理")).toBeLessThan(toolbar.indexOf("<a-input-search"));
    expect(source).toMatch(/\.toolbar-main\s*\{[^}]*display:\s*flex;/);
  });

  it("说明字段注册后不能删除，只能停用或隐藏；批量栏也叫“隐藏”", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "fields/index.vue"), "utf8");
    const batchBar = fs.readFileSync(path.resolve(__dirname, "fields/components/FieldBatchBar.vue"), "utf8");

    expect(source).toContain("字段注册后不能删除");
    expect(source).toContain("可以停用，或者隐藏");
    expect(batchBar).toContain("隐藏</a-button");
    expect(batchBar).not.toContain("删除</a-button");
  });
});
