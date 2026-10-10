import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const viewsRoot = path.resolve(__dirname, "../src/views");
const read = (relativePath: string) => fs.readFileSync(path.join(viewsRoot, relativePath), "utf8");

describe("host and service toolbar alignment", () => {
  it("uses one create-first host toolbar with compact spacing", () => {
    const hosts = read("container/ssh-hosts/ssh-hosts.vue");
    const hostWorkbench = read("ops/host-workbench/index.vue");

    expect(hosts).toContain('class="host-list-toolbar"');
    expect(hosts).not.toContain("<!-- 筛选区域 -->");
    expect(hosts).not.toContain("<!-- 操作按钮区域 -->");
    expect(hosts.indexOf("新增主机")).toBeLessThan(hosts.indexOf("搜索主机名称或地址"));
    expect(hosts.indexOf("搜索主机名称或地址")).toBeLessThan(hosts.indexOf("<span>查询</span>"));
    expect(hosts.indexOf("<span>查询</span>")).toBeLessThan(hosts.indexOf("<span>批量删除</span>"));
    expect(hosts).toMatch(/\.host-list-toolbar\s*\{[\s\S]*?margin-bottom:\s*var\(--moox-space-2\);/);
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
