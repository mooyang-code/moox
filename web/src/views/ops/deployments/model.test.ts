import { describe, expect, it } from "vitest";
import type { CatalogComponent, DeploymentHost, HostGatewayRoute, HostRoutesResponse } from "@/api/admin/types";
import { componentPorts, gatewayLabel, groupRoutes, loadHostRoutes, deploymentRows, routeSync } from "./model";
const host: DeploymentHost = { host_id: "a", address: "example.test", status: "enabled" };
const component: CatalogComponent = {
  id: "egress-proxy",
  name: "出口代理",
  binary: "moox-egress-proxy",
  scope: "any",
  health_port: 11012,
  ports: [11011],
  services: [
    {
      path: "test.Service",
      port: 11011,
      methods: [{ name: "Read", callers: ["reader"] }, { name: "Write" }]
    }
  ]
};
const snapshot: HostRoutesResponse = {
  host_id: "a",
  snapshot_schema_version: 1,
  compiled_at: String(Date.parse("2026-10-09T12:00:00Z")),
  gateway_status: { expected_hash: "new", applied_hash: "old", last_seen_at: String(Date.parse("2026-10-09T12:00:00Z")) }
};
describe("deployment presentation", () => {
  it("uses host plus component identity and never equates enablement with health", () => {
    const rows = deploymentRows(host, [{ host_id: "a", component_id: "egress-proxy", status: "enabled" }], [component], {
      components: [{ host_id: "b", component_id: "egress-proxy", status: "healthy" }]
    });
    expect(rows[0].status).toBe("unknown");
    expect(
      deploymentRows({ ...host, status: "disabled" }, [rows[0].deployment], [component], {
        components: [{ host_id: "a", component_id: "egress-proxy", status: "healthy" }]
      })[0].status
    ).toBe("disabled");
    expect(componentPorts(component)).toEqual([11011, 11012]);
    expect(
      componentPorts({
        ...component,
        ports: [9527],
        services: [
          { path: "trpc.moox.infra.Auth", port: 0, methods: [] },
          { path: "trpc.moox.ops.SecretMgr", port: 11108, methods: [] }
        ],
        health_port: 11010
      })
    ).toEqual([9527, 11010, 11108]);
  });
  it("uses server observation time for heartbeat age and shows waiting before first report", () => {
    expect(gatewayLabel(snapshot)).toBe("在线");
    expect(gatewayLabel({ ...snapshot, compiled_at: String(Date.parse("2026-10-09T12:02:01Z")) })).toBe("离线");
    expect(gatewayLabel({ ...snapshot, gateway_status: {} })).toBe("等待上报");
    // Admin 重启后尚未拉取或没拉取过时给 "0"
    expect(gatewayLabel({ ...snapshot, gateway_status: { last_seen_at: "0" } })).toBe("等待上报");
    expect(gatewayLabel()).toBe("状态未知");
  });
  it("compares the expected and applied snapshot", () => {
    expect(routeSync(snapshot)).toBe("待同步");
    expect(routeSync({ ...snapshot, gateway_status: { expected_hash: "new", applied_hash: "new" } })).toBe("已同步");
    expect(routeSync({ ...snapshot, gateway_status: {} })).toBe("同步状态未知");
  });
  it("filters callers and methods on the same route and preserves each method ACL", () => {
    const base = {
      component_id: "egress-proxy",
      service_path: "test.Service",
      address: "127.0.0.1:11011",
      timeout_ms: "5000",
      max_body_bytes: "4194304"
    };
    const routes: HostGatewayRoute[] = [
      { ...base, method: "Read", callers: ["reader"], read_only: true },
      { ...base, method: "Write", callers: ["writer"] }
    ];
    const groups = groupRoutes(routes);
    expect(groups).toHaveLength(1);
    expect(groups[0].port).toBe("11011");
    expect(groups[0].methods).toHaveLength(2);
    expect(groupRoutes(routes, "reader", "Write")).toEqual([]);
    expect(groupRoutes(routes, "writer", "write")[0].methods[0].callers).toEqual(["writer"]);
    expect(groupRoutes([...routes, { ...routes[0], timeout_ms: 9000 }])).toHaveLength(2);
  });
  it("bounds snapshot concurrency and keeps a per-host read failure visible", async () => {
    let active = 0;
    let maxActive = 0;
    const rows = Array.from({ length: 9 }, (_, i) => ({ ...host, host_id: String(i) }));
    const result = await loadHostRoutes(rows, async hostId => {
      active += 1;
      maxActive = Math.max(active, maxActive);
      await Promise.resolve();
      active -= 1;
      if (hostId === "2") throw new Error("unavailable");
      if (hostId === "3") return { ...snapshot, host_id: "wrong" };
      return { ...snapshot, host_id: hostId };
    });
    expect(maxActive).toBeLessThanOrEqual(4);
    expect(Object.keys(result.routes)).toHaveLength(7);
    expect(result.errors["2"]).toBe("unavailable");
    expect(result.errors["3"]).toContain("主机 ID");
  });
});
