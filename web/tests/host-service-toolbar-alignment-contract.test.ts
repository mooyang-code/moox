import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const viewsRoot = path.resolve(__dirname, "../src/views");
const read = (relativePath: string) => fs.readFileSync(path.join(viewsRoot, relativePath), "utf8");

describe("host and service toolbar alignment", () => {
  it("keeps the read-only host list hint compact above the table", () => {
    const hosts = read("container/ssh-hosts/ssh-hosts.vue");
    const hostWorkbench = read("ops/host-workbench/index.vue");

    expect(hosts).toContain('class="host-list-hint"');
    // 主机只读：不再有新增、编辑、删除入口，变更只通过 moox skill。
    for (const retired of ["新增主机", "批量删除", "编辑", "删除"]) expect(hosts).not.toContain(retired);
    expect(hosts).toContain("moox skill");
    expect(hosts.indexOf("host-list-hint")).toBeLessThan(hosts.indexOf("<a-table"));
    expect(hosts).toMatch(/\.host-list-hint\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-2\);/);
    expect(hostWorkbench).toMatch(/\.workbench-content\s*\{[\s\S]*?margin-top:\s*var\(--moox-space-3\);/);
  });

  it("keeps deployment filters compact without a create action", () => {
    const deployments = read("ops/deployments/index.vue");
    expect(deployments).toContain('class="filters"');
    expect(deployments).not.toContain("新增节点");
    expect(deployments).not.toContain("新增实例");
    expect(deployments).toMatch(/\.filters\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-2\);/);
  });
});
