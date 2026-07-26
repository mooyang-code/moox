import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const root = resolve(__dirname, "..");
const pages = {
  nodes: readFileSync(resolve(root, "src/views/ops/storage/nodes.vue"), "utf8"),
  archive: readFileSync(resolve(root, "src/views/ops/storage/archive.vue"), "utf8")
};

function pageActions(page: string, source: string): string {
  const match = source.match(/<div class="page-actions">([\s\S]*?)<\/div>/);
  if (!match) throw new Error(`${page} is missing a page-actions block`);
  return match[1];
}

describe("storage page actions contract", () => {
  it("keeps archive actions inline and does not add a DataNode create action", () => {
    expect(pages.nodes).not.toContain("新增节点");
    expect(pages.nodes).not.toContain("openCreate");
    expect(pageActions("archive", pages.archive)).toMatch(/datasetFilter[\s\S]*icon-search[\s\S]*查询/);
    expect(pages.archive).not.toContain("icon-refresh");
  });

  it("keeps the DataNode title explanation and fixed row operation affordances", () => {
    expect(pages.nodes).toContain('aria-label="数据节点说明"');
    expect(pages.nodes).toContain("部署流程拥有");
    expect(pages.nodes).toContain("不再经过独立路由层");
    expect(pages.nodes).toContain("只有已禁用且没有 Dataset 的节点才能删除");
    expect(pages.nodes).toContain('title="操作" :width="190"');
    expect(pages.nodes).toContain("display: flex;\n  flex-wrap: wrap;");
    expect(pages.nodes).toContain('path: "/collector/data-management"');
    expect(pages.nodes).toContain('query: { tab: "datasets", space_id: summary.space_id, dataset_id: summary.dataset_id }');
  });

  it("aligns the archive action row to the left", () => {
    expect(pages.archive).toMatch(/\.page-actions\s*\{[\s\S]*?justify-content:\s*flex-start;/);
  });
});
