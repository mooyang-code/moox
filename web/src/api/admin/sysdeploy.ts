import { isAlias, parseDocument, visit } from "yaml";
import { callControl } from "./http";
import type {
  CatalogResponse,
  ComponentCatalog,
  ComponentPlacement,
  DeploymentHost,
  DeploymentStatus,
  HostRoutesResponse,
  PageReq,
  PageResult,
  ServiceDirectory
} from "./types";

export function getCatalog() {
  return callControl<Record<string, never>, CatalogResponse>("sysdeploy", "GetCatalog", {});
}
export function listDeploymentHosts(req: { host_id?: string; status?: DeploymentStatus; page?: PageReq } = {}) {
  return callControl<typeof req, { hosts?: DeploymentHost[]; page_result?: PageResult }>("sysdeploy", "ListHosts", req);
}
export function listPlacements(req: { host_id?: string; component_id?: string; status?: DeploymentStatus; page?: PageReq } = {}) {
  return callControl<typeof req, { placements?: ComponentPlacement[]; page_result?: PageResult }>(
    "sysdeploy",
    "ListPlacements",
    req
  );
}
export function getHostRoutes(hostId: string) {
  return callControl<{ host_id: string }, HostRoutesResponse>("sysdeploy", "GetHostRoutes", { host_id: hostId });
}
export function getDirectory() {
  return callControl<Record<string, never>, { directory?: ServiceDirectory }>("sysdeploy", "GetDirectory", {});
}
export function setHostStatus(hostId: string, status: DeploymentStatus) {
  return callControl<{ host_id: string; status: DeploymentStatus }, Record<string, never>>("sysdeploy", "SetHostStatus", {
    host_id: hostId,
    status
  });
}
export function setPlacementStatus(hostId: string, componentId: string, status: DeploymentStatus) {
  return callControl<{ host_id: string; component_id: string; status: DeploymentStatus }, Record<string, never>>(
    "sysdeploy",
    "SetPlacementStatus",
    { host_id: hostId, component_id: componentId, status }
  );
}

// Admin caps each page at 100. Do not silently hide later hosts or placements.
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
export function listAllPlacements() {
  return allPages(
    async page => {
      const result = await listPlacements({ page: { page, size: 100 } });
      return { items: result.placements || [], page_result: result.page_result };
    },
    row => `${row.host_id}\0${row.component_id}`
  );
}
export function decodeCatalog(response: CatalogResponse): ComponentCatalog {
  if (!response.control_host_id || !/^[a-f0-9]{64}$/.test(response.sha256)) throw new Error("组件目录缺少有效的控制主机或摘要");
  if (response.catalog_yaml.length > 2 * 1024 * 1024) throw new Error("组件目录超过大小限制");
  const doc = parseDocument(response.catalog_yaml, { uniqueKeys: true });
  if (doc.errors.length) throw new Error("组件目录 YAML 无效");
  visit(doc, (_key, node) => {
    if (isAlias(node)) throw new Error("组件目录不允许 YAML 别名");
  });
  const value = doc.toJS({ maxAliasCount: 0 }) as ComponentCatalog;
  if (!value || value.version !== 1 || !Array.isArray(value.components) || !Array.isArray(value.principals))
    throw new Error("组件目录结构无效");
  const ids = new Set<string>();
  for (const component of value.components) {
    if (
      !component ||
      typeof component.id !== "string" ||
      !component.id ||
      ids.has(component.id) ||
      typeof component.name !== "string" ||
      typeof component.binary !== "string" ||
      typeof component.protected !== "boolean" ||
      !component.health ||
      !["host", "control", "any"].includes(component.scope) ||
      !["single", "multi"].includes(component.replicas)
    )
      throw new Error("组件目录含无效或重复组件");
    ids.add(component.id);
  }
  return value;
}
