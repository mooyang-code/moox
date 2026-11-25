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
});
