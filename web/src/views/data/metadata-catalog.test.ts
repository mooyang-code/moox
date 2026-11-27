import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("metadata catalog pages", () => {
  it("keeps source tabs, source search and subject tag/member navigation", () => {
    const sourcesPage = fs.readFileSync(path.resolve(__dirname, "sources/index.vue"), "utf8");
    expect(sourcesPage).not.toContain("<h2>数据源</h2>");
    expect(sourcesPage).toContain("PageTitleTabs");
    expect(sourcesPage).toContain('label: "数据源"');
    expect(sourcesPage).toContain('label: "字段定义"');

    const sourcesTab = fs.readFileSync(path.resolve(__dirname, "sources/sources-tab.vue"), "utf8");
    const toolbar = sourcesTab.slice(
      sourcesTab.indexOf('<div class="page-head">'),
      sourcesTab.indexOf("</div>", sourcesTab.indexOf('<div class="page-head">')) + 6
    );
    expect(toolbar).toContain("<a-input-search");
    expect(toolbar).toContain("新建");
    expect(toolbar.indexOf("<a-input-search")).toBeLessThan(toolbar.indexOf("新建"));
    expect(sourcesTab).toContain('@search="onSearch"');
    expect(sourcesTab).toContain('@clear="onSearch"');
    expect(sourcesTab).toContain("keyword: searchKeyword.value.trim() || undefined");

    const subjects = fs.readFileSync(path.resolve(__dirname, "subjects/index.vue"), "utf8");
    expect(subjects).not.toContain("<h2>数据对象</h2>");
    expect(subjects).toContain("PageTitleTabs");
    expect(subjects).toContain('label: "标签"');
    expect(subjects).toContain('label: "标签成员"');
  });
});
