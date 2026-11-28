import { call } from "@/api/http";
import type {
  CatalogResponse,
  ComponentDeployment,
  DeploymentHost,
  DeploymentStatus,
  HostRoutesResponse,
  PageReq,
  PageResult
} from "./types";

export function getCatalog() {
  return call<Record<string, never>, CatalogResponse>("sysdeploy", "GetCatalog", {});
}
export function listDeploymentHosts(req: { host_id?: string; status?: DeploymentStatus; page?: PageReq } = {}) {
  return call<typeof req, { hosts?: DeploymentHost[]; page_result?: PageResult }>("sysdeploy", "ListHosts", req);
}
export function listDeployments(
  req: { host_id?: string; component_id?: string; status?: DeploymentStatus; page?: PageReq } = {}
) {
  return call<typeof req, { deployments?: ComponentDeployment[]; page_result?: PageResult }>("sysdeploy", "ListDeployments", req);
}
export function getHostRoutes(hostId: string) {
  return call<{ host_id: string }, HostRoutesResponse>("sysdeploy", "GetHostRoutes", { host_id: hostId });
}
// Admin caps each page at 100. Do not silently hide later hosts or deployments.
async function allPages<T>(read: (page: number) => Promise<{ items: T[]; page_result?: PageResult }>, key: (row: T) => string) {
  const rows: T[] = [];
  const keys = new Set<string>();
  for (let page = 1; ; page += 1) {
    const result = await read(page);
    for (const row of result.items) {
      const id = key(row);
      if (keys.has(id)) throw new Error("部署清单在分页期间变化，请刷新重试");
      keys.add(id);
      rows.push(row);
    }
    if (rows.length > 1500) throw new Error("部署清单超过 1500 项，无法完整展示");
    if (!result.page_result?.has_more) return rows;
    if (result.items.length === 0) throw new Error("部署清单分页返回空页，请刷新重试");
  }
}
export function listAllDeploymentHosts() {
  return allPages(
    async page => {
      const result = await listDeploymentHosts({ page: { page, size: 100 } });
      return { items: result.hosts || [], page_result: result.page_result };
    },
    row => row.host_id
  );
}
export function listAllDeployments() {
  return allPages(
    async page => {
      const result = await listDeployments({ page: { page, size: 100 } });
      return { items: result.deployments || [], page_result: result.page_result };
    },
    row => `${row.host_id}\0${row.component_id}`
  );
}
