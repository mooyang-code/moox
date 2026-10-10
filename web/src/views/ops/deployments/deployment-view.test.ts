import { describe, expect, it } from "vitest";
import type { CatalogComponent, DeployHost, DeployPlacement } from "@/api/admin/types";
import type { HealthComponent } from "@/api/monitor";
import {
  aclSummary,
  buildHostGroups,
  filterHostGroups,
  filterRoutes,
  formatBytes,
  formatTimeout,
  gatewayAttention,
  gatewayStateText,
  routeCallers,
  summarizeGroups,
  syncText,
  upstreamPort,
  type DeploymentSources
} from "./deployment-view";

const catalog: CatalogComponent[] = [
  { id: "host-gateway", name: "主机网关", scope: "host", services: [] },
  { id: "host-agent", name: "主机采集器", scope: "host" },
  { id: "admin", name: "管理后台", protected: true, services: [{ path: "trpc.moox.ops.SysDeploy", port: 11109 }] },
  {
    id: "storage-view",
    name: "存储视图服务",
    services: [
      {
        path: "trpc.moox.storage.View",
        port: 20103,
        methods: [
          { name: "Query", read_only: true, callers: ["console", "strategy"] },
          { name: "Rebuild", callers: ["moox-cli"] }
        ]
      }
    ]
  },
  { id: "access", name: "外部接入", services: [] }
];

const hosts: DeployHost[] = [
  { host_id: "storage", address: "146.56.196.204", status: "enabled", gateway: { state: "online", synced: true } },
  {
    host_id: "control",
    address: "106.53.107.122",
    status: "enabled",
    protected: true,
    gateway: { state: "online", synced: false, out_of_sync_since: "2026-10-09T08:00:00Z" }
  },
  { host_id: "compute-1", address: "43.132.204.177", status: "disabled", gateway: { state: "offline" } }
];

const placements: DeployPlacement[] = [
  { host_id: "control", component_id: "admin", status: "enabled", protected: true },
  { host_id: "control", component_id: "host-gateway", status: "enabled", protected: true, host_component: true },
  { host_id: "storage", component_id: "access", status: "disabled" },
  { host_id: "storage", component_id: "storage-view", status: "enabled" },
  { host_id: "storage", component_id: "host-agent", status: "enabled", host_component: true },
  { host_id: "compute-1", component_id: "access", status: "enabled" }
];

const health: HealthComponent[] = [
  { host_id: "control", component_id: "admin", status: "healthy", reason: "正常" },
  { host_id: "storage", component_id: "storage-view", status: "down", reason: "健康探测失败" }
];

function sources(overrides: Partial<DeploymentSources> = {}): DeploymentSources {
  return { hosts, placements, catalog, health, unregistered: [], ...overrides };
}

describe("deployment view", () => {
  it("groups placements by host with control first and components in catalog order", () => {
    const groups = buildHostGroups(sources());
    expect(groups.map(group => group.hostId)).toEqual(["control", "compute-1", "storage"]);
    const storage = groups[2];
    expect(storage.rows.map(row => row.componentId)).toEqual(["storage-view", "access"]);
    expect(storage.hostRows.map(row => row.componentId)).toEqual(["host-agent"]);
    expect(storage.rows[0]).toMatchObject({
      name: "存储视图服务",
      healthStatus: "down",
      services: [{ path: "trpc.moox.storage.View", port: 20103 }]
    });
    expect(storage.rows[1]).toMatchObject({ enabled: false, healthStatus: "disabled" });
    expect(storage.hostRows[0].healthStatus).toBe("unknown");
    expect(storage.attention).toBe(true);
    expect(groups[0].rows[0].protected).toBe(true);
    expect(groups[0].attention).toBe(true);
    expect(groups[1]).toMatchObject({ enabled: false, gatewayState: "offline" });
    expect(groups[1].rows[0].healthStatus).toBe("disabled");
  });

  it("filters by keyword, attention and disabled state", () => {
    const groups = buildHostGroups(sources());
    const all = filterHostGroups(groups, { search: "", filter: "all", showDisabled: true });
    expect(all).toHaveLength(3);
    const hidden = filterHostGroups(groups, { search: "", filter: "all", showDisabled: false });
    expect(hidden.map(group => group.hostId)).toEqual(["control", "storage"]);
    expect(hidden[1].rows.map(row => row.componentId)).toEqual(["storage-view"]);
    const attention = filterHostGroups(groups, { search: "", filter: "attention", showDisabled: true });
    expect(attention.flatMap(group => group.rows.map(row => row.key))).toEqual(["storage:storage-view"]);
    expect(attention.map(group => group.hostId)).toContain("control");
    const disabled = filterHostGroups(groups, { search: "", filter: "disabled", showDisabled: false });
    expect(disabled.flatMap(group => group.rows.map(row => row.key))).toEqual(["compute-1:access", "storage:access"]);
    const search = filterHostGroups(groups, { search: "SysDeploy", filter: "all", showDisabled: true });
    expect(search.flatMap(group => group.rows.map(row => row.key))).toEqual(["control:admin"]);
    const byHost = filterHostGroups(groups, { search: "storage", filter: "all", showDisabled: true });
    expect(byHost.find(group => group.hostId === "storage")?.rows).toHaveLength(2);
  });

  it("summarizes deployments and keeps unregistered processes on their host", () => {
    const groups = buildHostGroups(
      sources({ unregistered: [{ host_id: "storage", component_id: "trade", instance_id: "trade@storage" }] })
    );
    expect(summarizeGroups(groups)).toEqual({ hosts: 3, deployments: 6, attention: 1, disabled: 2 });
    expect(groups[2].unregistered.map(item => item.instance_id)).toEqual(["trade@storage"]);
  });

  it("describes host gateway state and route sync", () => {
    expect(gatewayStateText("never_reported")).toBe("从未上报");
    expect(gatewayStateText("conflict")).toBe("重复实例");
    expect(syncText({ state: "online", synced: true })).toBe("已同步");
    expect(syncText({ state: "online", synced: false, out_of_sync_since: "2026-10-09T08:00:00Z" })).toMatch(/^待同步（自 /);
    expect(syncText({ state: "never_reported" })).toBe("—");
    expect(gatewayAttention({ state: "online", synced: false })).toBe(true);
    expect(gatewayAttention({ state: "never_reported" })).toBe(false);
  });

  it("summarizes the catalog ACL and formats route limits", () => {
    expect(aclSummary(catalog[3])).toEqual([
      { path: "trpc.moox.storage.View", port: 20103, methods: 2, readOnly: 1, callers: ["console", "moox-cli", "strategy"] }
    ]);
    expect(upstreamPort("127.0.0.1:11109")).toBe(11109);
    expect(upstreamPort("")).toBeUndefined();
    expect(formatBytes(4 << 20)).toBe("4 MiB");
    expect(formatBytes("1536")).toBe("1.5 KiB");
    expect(formatBytes(0)).toBe("—");
    expect(formatTimeout("120000")).toBe("120s");
    expect(formatTimeout(1500)).toBe("1500ms");
  });

  it("filters routes by caller and method", () => {
    const routes = [
      { service_path: "trpc.moox.ops.SysDeploy", methods: ["ListHosts", "SetHostStatus"], callers: ["console", "moox-cli"] },
      { service_path: "trpc.moox.storage.View", methods: ["Query"], callers: ["strategy"] }
    ];
    expect(routeCallers(routes)).toEqual(["console", "moox-cli", "strategy"]);
    expect(filterRoutes(routes, "strategy", "")).toEqual([routes[1]]);
    expect(filterRoutes(routes, "", "sethost")).toEqual([routes[0]]);
    expect(filterRoutes(routes, "console", "query")).toEqual([]);
  });
});
