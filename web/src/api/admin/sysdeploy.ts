import { callControl } from "./http";
import type { Catalog, DeployHost, DeployPlacement, HostRoutes } from "./types";

// 服务部署（SysDeploy v2）：主机与部署由 moox-cli 按 moox.toml 同步，管理台只能查看和启用 / 停用。
const SERVICE = "sysdeploy";

export const sysdeployApi = {
  getCatalog() {
    return callControl<Record<string, never>, Catalog>(SERVICE, "GetCatalog", {});
  },
  listHosts() {
    return callControl<Record<string, never>, { hosts?: DeployHost[] }>(SERVICE, "ListHosts", {});
  },
  listPlacements(req: { host_id?: string; component_id?: string } = {}) {
    return callControl<typeof req, { placements?: DeployPlacement[] }>(SERVICE, "ListPlacements", req);
  },
  getHostRoutes(hostId: string) {
    return callControl<{ host_id: string }, HostRoutes>(SERVICE, "GetHostRoutes", { host_id: hostId });
  },
  setHostStatus(hostId: string, status: "enabled" | "disabled") {
    return callControl<{ host_id: string; status: string }, { host?: DeployHost }>(SERVICE, "SetHostStatus", {
      host_id: hostId,
      status
    });
  },
  setPlacementStatus(hostId: string, componentId: string, status: "enabled" | "disabled") {
    return callControl<{ host_id: string; component_id: string; status: string }, { placement?: DeployPlacement }>(
      SERVICE,
      "SetPlacementStatus",
      { host_id: hostId, component_id: componentId, status }
    );
  }
};
