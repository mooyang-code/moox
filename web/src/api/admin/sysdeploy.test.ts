import { readFileSync } from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { callControl } from "./http";
import {
  decodeCatalog,
  getHostRoutes,
  listAllDeploymentHosts,
  listAllPlacements,
  setHostStatus,
  setPlacementStatus
} from "./sysdeploy";
vi.mock("./http", () => ({ callControl: vi.fn() }));
const call = vi.mocked(callControl);
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
    call.mockResolvedValue({ placements: [], page_result: { has_more: true } });
    await expect(listAllPlacements()).rejects.toThrow("空页");
    call.mockResolvedValue({
      placements: [{ host_id: "a", component_id: "collector", status: "enabled" }],
      page_result: { has_more: true }
    });
    await expect(listAllPlacements()).rejects.toThrow("分页期间变化");
  });
  it("keeps placement identity separate for the same component on different hosts", async () => {
    call.mockResolvedValue({
      placements: [
        { host_id: "a", component_id: "access", status: "enabled" },
        { host_id: "b", component_id: "access", status: "disabled" }
      ]
    });
    expect(await listAllPlacements()).toHaveLength(2);
  });
  it("sends only the v2 status change and route requests", async () => {
    call.mockResolvedValue({});
    await setHostStatus("c-main", "disabled");
    await setPlacementStatus("storage", "storage-primary", "enabled");
    await getHostRoutes("storage");
    expect(call.mock.calls).toEqual([
      ["sysdeploy", "SetHostStatus", { host_id: "c-main", status: "disabled" }],
      ["sysdeploy", "SetPlacementStatus", { host_id: "storage", component_id: "storage-primary", status: "enabled" }],
      ["sysdeploy", "GetHostRoutes", { host_id: "storage" }]
    ]);
  });
  it("decodes the actual shared catalog used by Admin", () => {
    const catalog_yaml = readFileSync(path.resolve(__dirname, "../../../../packages/servicecatalog/catalog.yaml"), "utf8");
    const result = decodeCatalog({ catalog_yaml, control_host_id: "control", sha256: "a".repeat(64) });
    expect(result.components.find(item => item.id === "host-gateway")?.protected).toBe(true);
    expect(result.components.find(item => item.id === "storage-primary")?.services?.length).toBeGreaterThan(0);
  });
  it("requires explicit protection metadata and rejects YAML aliases and duplicate keys", () => {
    const response = {
      control_host_id: "arbitrary-control-id",
      sha256: "a".repeat(64),
      catalog_yaml: "version: 1\ncomponents: []\nprincipals: []\n"
    };
    expect(decodeCatalog(response).version).toBe(1);
    expect(() => decodeCatalog({ ...response, control_host_id: "" })).toThrow("控制主机");
    expect(() => decodeCatalog({ ...response, catalog_yaml: "version: 1\nversion: 1\ncomponents: []\nprincipals: []" })).toThrow(
      "YAML"
    );
    expect(() => decodeCatalog({ ...response, catalog_yaml: "version: 1\ncomponents: &x []\nprincipals: *x" })).toThrow();
  });
});
