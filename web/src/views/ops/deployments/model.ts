import type {
  CatalogComponent,
  ComponentPlacement,
  DeploymentHost,
  HostGatewayRoute,
  HostRoutesResponse
} from "@/api/admin/types";
import type { HealthComponent, HealthGatewaySignal, HealthOverview, HealthStatus } from "@/api/monitor";

export interface PlacementRow {
  key: string;
  placement: ComponentPlacement;
  definition?: CatalogComponent;
  health?: HealthComponent;
  status: HealthStatus;
}
export function placementKey(hostId: string, componentId: string) {
  return `${hostId}\0${componentId}`;
}
export function placementRows(
  host: DeploymentHost,
  placements: ComponentPlacement[],
  components: CatalogComponent[],
  overview?: HealthOverview
): PlacementRow[] {
  const definitions = new Map(components.map(item => [item.id, item]));
  const health = new Map(
    (overview?.components || []).map(item => [placementKey(item.host_id || "", item.component_id || ""), item])
  );
  return placements
    .filter(item => item.host_id === host.host_id)
    .map(placement => {
      const key = placementKey(placement.host_id, placement.component_id);
      const enabled = host.status === "enabled" && placement.status === "enabled";
      const observed = health.get(key);
      // Admin enablement may be newer than Monitor's last topology sample.
      const currentHealth = enabled && observed?.status === "disabled" ? undefined : observed;
      return {
        key,
        placement,
        definition: definitions.get(placement.component_id),
        health: currentHealth,
        status: enabled ? currentHealth?.status || "unknown" : "disabled"
      };
    });
}
export function canTogglePlacement(host: DeploymentHost, row: PlacementRow) {
  return host.status === "enabled" && !!row.definition && !row.definition.protected;
}
export function componentPorts(component?: CatalogComponent) {
  return [
    ...new Set([
      ...(component?.ports || []),
      ...(component?.services || []).map(item => item.port),
      ...(component?.health.port ? [component.health.port] : [])
    ])
  ].sort((a, b) => a - b);
}
export function gatewayLabel(snapshot?: HostRoutesResponse) {
  if (!snapshot) return "状态未知";
  const status = snapshot.gateway_status;
  if (status?.conflict_instance_id) return "重复实例";
  if (!status?.last_seen_at) return "从未上报";
  const seen = Date.parse(status.last_seen_at);
  const observed = Date.parse(snapshot.compiled_at);
  if (!Number.isFinite(seen) || !Number.isFinite(observed)) return "状态未知";
  return observed - seen > 120000 ? "离线" : "在线";
}
export function routeSync(snapshot?: HostRoutesResponse, signal?: HealthGatewaySignal) {
  const status = snapshot?.gateway_status;
  if (!status?.expected_hash) return { label: "同步状态未知", pendingSince: "" };
  if (status.expected_hash === status.applied_hash) return { label: "已同步", pendingSince: "" };
  const sameObservation =
    signal?.expected_hash === status.expected_hash && (signal?.applied_hash || "") === (status.applied_hash || "");
  return { label: "待同步", pendingSince: sameObservation ? signal?.pending_since || "" : "" };
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
