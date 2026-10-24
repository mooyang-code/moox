import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const root = resolve(__dirname, "..");
const pages = {
  nodes: readFileSync(resolve(root, "src/views/ops/storage/nodes.vue"), "utf8")
};

describe("storage page actions contract", () => {
  it("does not add a DataNode create action", () => {
    expect(pages.nodes).not.toContain("新增节点");
    expect(pages.nodes).not.toContain("openCreate");
  });

  it("keeps the DataNode title explanation as a read-only page", () => {
    expect(pages.nodes).toContain('aria-label="数据节点说明"');
    expect(pages.nodes).toContain("管理台只读");
    expect(pages.nodes).toContain("moox skill");
    expect(pages.nodes).toContain("Dataset 直接绑定 DataNode");
    for (const retired of ["openEdit", "updateDataNode", "deleteDataNode", "data-node-edit-modal"]) {
      expect(pages.nodes).not.toContain(retired);
    }
    expect(pages.nodes).toContain('title="操作" :width="90"');
    expect(pages.nodes).toContain("display: flex;\n  flex-wrap: wrap;");
    expect(pages.nodes).toContain('path: "/collector/tasks"');
    expect(pages.nodes).toContain('query: { tab: "results", space_id: summary.space_id }');
  });

  it("uses a modal for DataNode details instead of a side drawer", () => {
    expect(pages.nodes).toContain('data-testid="data-node-detail-modal"');
    expect(pages.nodes).not.toContain('data-testid="data-node-detail-drawer"');
    expect(pages.nodes).toMatch(/<a-modal[\s\S]*data-testid="data-node-detail-modal"[\s\S]*width="1000px"/);
    expect(pages.nodes).toContain('class="detail-datasets-list"');
    expect(pages.nodes).toContain("<a-pagination");
    expect(pages.nodes).toContain(":pagination=\"false\"");
    expect(pages.nodes).toContain("pageSize: 10");
    expect(pages.nodes).toContain("showPageSize: false");
  });

});
