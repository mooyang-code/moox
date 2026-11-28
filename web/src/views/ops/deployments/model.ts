import type {
  CatalogComponent,
  CatalogMethod,
  ComponentDeployment,
  DeploymentHost,
  HostGatewayRoute,
  HostRoutesResponse
} from "@/api/admin/types";
import type { HealthComponent, HealthOverview, HealthStatus } from "@/api/monitor";
import { toInstantMs } from "@/utils/time-zone";

export interface DeploymentRow {
  key: string;
  deployment: ComponentDeployment;
  definition?: CatalogComponent;
  health?: HealthComponent;
  status: HealthStatus;
}
/** 受保护组件由组件身份确定（不可停用或移除）：控制机的 admin 与每台主机的 host-gateway。 */
export function isProtectedComponent(componentId: string): boolean {
  return componentId === "admin" || componentId === "host-gateway";
}
/** 方法的全部调用方（后端 GetCatalog 已按目录规则展开 operators 与 callers）。 */
export function methodCallerIds(method: CatalogMethod): string[] {
  return method.callers || [];
}
export function deploymentKey(hostId: string, componentId: string) {
  return `${hostId}\0${componentId}`;
}
export function deploymentRows(
  host: DeploymentHost,
  deployments: ComponentDeployment[],
  components: CatalogComponent[],
  overview?: HealthOverview
): DeploymentRow[] {
  const definitions = new Map(components.map(item => [item.id, item]));
  const health = new Map(
    (overview?.components || []).map(item => [deploymentKey(item.host_id || "", item.component_id || ""), item])
  );
  return deployments
    .filter(item => item.host_id === host.host_id)
    .map(deployment => {
      const key = deploymentKey(deployment.host_id, deployment.component_id);
      const enabled = host.status === "enabled" && deployment.status === "enabled";
      const observed = health.get(key);
      // Admin enablement may be newer than Monitor's last topology sample.
      const currentHealth = enabled && observed?.status === "disabled" ? undefined : observed;
      return {
        key,
        deployment,
        definition: definitions.get(deployment.component_id),
        health: currentHealth,
        status: enabled ? currentHealth?.status || "unknown" : "disabled"
      };
    });
}
export function componentPorts(component?: CatalogComponent) {
  return [
    ...new Set([
      ...(component?.ports || []).filter(port => port > 0),
      ...(component?.services || []).map(item => item.port).filter(port => port > 0),
      ...(component?.health_port ? [component.health_port] : [])
    ])
  ].sort((a, b) => a - b);
}
export function gatewayLabel(snapshot?: HostRoutesResponse) {
  if (!snapshot) return "状态未知";
  const status = snapshot.gateway_status;
  const seen = toInstantMs(status?.last_seen_at);
  if (seen === null) return "等待上报";
  const observed = toInstantMs(snapshot.compiled_at);
  if (observed === null) return "状态未知";
  return observed - seen > 120000 ? "离线" : "在线";
}
// 已应用快照最多落后期望快照一次拉取（约 5 秒），所以“待同步”只在刚改完拓扑时短暂出现。
export function routeSync(snapshot?: HostRoutesResponse) {
  const status = snapshot?.gateway_status;
  if (!status?.expected_hash) return "同步状态未知";
  return status.expected_hash === status.applied_hash ? "已同步" : "待同步";
}
export interface RouteGroup {
  key: string;
  componentId: string;
  service: string;
  address: string;
  port: string;
  timeout: number | string;
  maxBody: number | string;
  callers: string[];
  methods: HostGatewayRoute[];
}
export function groupRoutes(routes: HostGatewayRoute[], caller = "", method = ""): RouteGroup[] {
  const groups = new Map<string, RouteGroup>();
  const needle = method.trim().toLowerCase();
  for (const route of routes) {
    if (caller && !route.callers?.includes(caller)) continue;
    if (needle && !route.method.toLowerCase().includes(needle)) continue;
    const key = JSON.stringify([
      route.component_id,
      route.service_path,
      route.address,
      route.timeout_ms || 0,
      route.max_body_bytes || 0
    ]);
    let group = groups.get(key);
    if (!group) {
      group = {
        key,
        componentId: route.component_id,
        service: route.service_path,
        address: route.address,
        port: route.address.match(/:(\d+)$/)?.[1] || route.address,
        timeout: route.timeout_ms || 0,
        maxBody: route.max_body_bytes || 0,
        callers: [],
        methods: []
      };
      groups.set(key, group);
    }
    group.methods.push(route);
    group.callers = [...new Set([...group.callers, ...(route.callers || [])])].sort();
  }
  return [...groups.values()].sort((a, b) => a.service.localeCompare(b.service) || a.address.localeCompare(b.address));
}

// Compile snapshots with bounded concurrency rather than flooding Admin for a
// large deployment. A failure stays attached to its host, never an empty route set.
export async function loadHostRoutes(hosts: DeploymentHost[], read: (hostId: string) => Promise<HostRoutesResponse>) {
  const routes: Record<string, HostRoutesResponse> = {};
  const errors: Record<string, string> = {};
  let next = 0;
  await Promise.all(
    Array.from({ length: Math.min(4, hosts.length) }, async () => {
      while (next < hosts.length) {
        const host = hosts[next++];
        try {
          const result = await read(host.host_id);
          if (result.host_id !== host.host_id) throw new Error("返回的主机 ID 与请求不一致");
          routes[host.host_id] = result;
        } catch (error) {
          errors[host.host_id] = error instanceof Error ? error.message : String(error);
        }
      }
    })
  );
  return { routes, errors };
}
