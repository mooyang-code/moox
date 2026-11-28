import { beforeEach, describe, expect, it, vi } from "vitest";
import { call as callAdmin } from "@/api/http";
import { getCatalog, getHostRoutes, listAllDeploymentHosts, listAllDeployments } from "./sysdeploy";
vi.mock("@/api/http", () => ({ call: vi.fn() }));
const call = vi.mocked(callAdmin);
beforeEach(() => {
  call.mockReset();
});
describe("SysDeploy v2", () => {
  it("loads every Admin page with a supported page size", async () => {
    call.mockImplementation(async (_service, _method, req) => {
      const page = (req as { page: { page: number } }).page.page;
      return {
        hosts: Array.from({ length: page === 1 ? 100 : 3 }, (_, i) => ({
          host_id: `${page}-${i}`,
          address: "example.test",
          status: "enabled"
        })),
        page_result: { page, size: 100, total: 103, has_more: page === 1 }
      };
    });
    expect(await listAllDeploymentHosts()).toHaveLength(103);
    expect(call).toHaveBeenNthCalledWith(2, "sysdeploy", "ListHosts", { page: { page: 2, size: 100 } });
  });
  it("rejects broken or changing pagination rather than displaying a partial topology", async () => {
    call.mockResolvedValue({ deployments: [], page_result: { has_more: true } });
    await expect(listAllDeployments()).rejects.toThrow("空页");
    call.mockResolvedValue({
      deployments: [{ host_id: "a", component_id: "collector", status: "enabled" }],
      page_result: { has_more: true }
    });
    await expect(listAllDeployments()).rejects.toThrow("分页期间变化");
  });
  it("keeps deployment identity separate for the same component on different hosts", async () => {
    call.mockResolvedValue({
      deployments: [
        { host_id: "a", component_id: "egress-proxy", status: "enabled" },
        { host_id: "b", component_id: "egress-proxy", status: "disabled" }
      ]
    });
    expect(await listAllDeployments()).toHaveLength(2);
  });
  // 主机与部署的写操作只能通过 moox skill（moox-cli）：控制台接口层不再提供启停调用。
  it("exposes only read requests and no status mutation", async () => {
    call.mockResolvedValue({});
    await getHostRoutes("storage");
    expect(call.mock.calls).toEqual([["sysdeploy", "GetHostRoutes", { host_id: "storage" }]]);
    const api = await import("./sysdeploy");
    expect(api).not.toHaveProperty("setHostStatus");
    expect(api).not.toHaveProperty("setDeploymentStatus");
  });
  it("reads the structured catalog directly from GetCatalog without client-side YAML decoding", async () => {
    const structured = {
      control_host_id: "control",
      sha256: "a".repeat(64),
      catalog: {
        version: 2,
        identities: [{ id: "scf-collector", external: true }],
        components: [{ id: "host-gateway", name: "主机网关", binary: "moox-host-gateway", scope: "host", health_port: 11012 }]
      }
    };
    call.mockResolvedValue(structured);
    const result = await getCatalog();
    expect(call).toHaveBeenCalledWith("sysdeploy", "GetCatalog", {});
    expect(result.catalog?.components?.[0]?.id).toBe("host-gateway");
    const api = await import("./sysdeploy");
    expect(api).not.toHaveProperty("decodeCatalog");
  });
});
