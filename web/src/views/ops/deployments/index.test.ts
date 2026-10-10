import fs from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { callControl } from "@/api/admin/http";
import { sysdeployApi } from "@/api/admin/sysdeploy";

vi.mock("@/api/admin/http", () => ({ callControl: vi.fn() }));

const mockedCallControl = vi.mocked(callControl);
const read = (file: string) => fs.readFileSync(path.resolve(__dirname, file), "utf8");

describe("deployments page", () => {
  beforeEach(() => mockedCallControl.mockReset());

  it("uses only the SysDeploy v2 view and enable / disable methods", async () => {
    mockedCallControl.mockResolvedValue({});
    await sysdeployApi.getCatalog();
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "GetCatalog", {});
    await sysdeployApi.listHosts();
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "ListHosts", {});
    await sysdeployApi.listPlacements();
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "ListPlacements", {});
    await sysdeployApi.getHostRoutes("storage");
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "GetHostRoutes", { host_id: "storage" });
    await sysdeployApi.setHostStatus("storage", "disabled");
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "SetHostStatus", { host_id: "storage", status: "disabled" });
    await sysdeployApi.setPlacementStatus("storage", "access", "enabled");
    expect(mockedCallControl).toHaveBeenLastCalledWith("sysdeploy", "SetPlacementStatus", {
      host_id: "storage",
      component_id: "access",
      status: "enabled"
    });
    expect(Object.keys(sysdeployApi).sort()).toEqual([
      "getCatalog",
      "getHostRoutes",
      "listHosts",
      "listPlacements",
      "setHostStatus",
      "setPlacementStatus"
    ]);
  });

  it("offers only viewing and enable / disable, never creating hosts or deployments", () => {
    const services = read("services-tab.vue");
    for (const token of ["显示已停用", "受保护", "停用主机", "未登记的进程", "moox.toml", "主机网关", "路由", "before-change"]) {
      expect(services).toContain(token);
    }
    expect(services).toContain('v-if="!group.host.protected"');
    for (const forbidden of ["新增", "SyncHostPlacements", "DeleteHost", "删除部署"]) {
      expect(services).not.toContain(forbidden);
    }
    const routes = read("routes-tab.vue");
    for (const token of ["快照版本", "生成时间", "同步状态", "上游端口", "调用方", "包体上限", "expand-row"]) {
      expect(routes).toContain(token);
    }
    const drawer = read("component-drawer.vue");
    for (const token of ["组件目录", "tRPC 服务与调用方", "健康检查", "部署", "运行状态"]) {
      expect(drawer).toContain(token);
    }
    expect(read("index.vue")).toContain('label: "网关路由"');
  });
});
