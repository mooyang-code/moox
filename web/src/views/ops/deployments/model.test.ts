import { describe, expect, it } from "vitest";
import type { CatalogComponent, DeploymentHost, HostGatewayRoute, HostRoutesResponse } from "@/api/admin/types";
import { canTogglePlacement, componentPorts, gatewayLabel, groupRoutes, loadHostRoutes, placementRows, routeSync } from "./model";
const host: DeploymentHost = { host_id: "a", address: "example.test", status: "enabled" };
const component: CatalogComponent = {
  id: "access",
  name: "外部接入",
  binary: "moox-access",
  scope: "any",
  replicas: "multi",
  protected: false,
  health: { kind: "readyz", port: 11012 },
  ports: [11011],
  services: [{ path: "test.Service", port: 11011, methods: ["Read", "Write"], acl: [{ methods: ["Read"], callers: ["reader"] }] }]
};
const snapshot: HostRoutesResponse = {
  host_id: "a",
  definition_hash: "definition",
  snapshot_schema_version: 1,
  compiled_at: "2026-10-09T12:00:00Z",
  gateway_status: { expected_hash: "new", applied_hash: "old", last_seen_at: "2026-10-09T12:00:00Z" }
};
describe("deployment presentation", () => {
  it("uses host plus component identity and never equates enablement with health", () => {
    const rows = placementRows(host, [{ host_id: "a", component_id: "access", status: "enabled" }], [component], {
      components: [{ host_id: "b", component_id: "access", status: "healthy" }]
    });
    expect(rows[0].status).toBe("unknown");
    expect(canTogglePlacement(host, rows[0])).toBe(true);
    expect(canTogglePlacement(host, { ...rows[0], definition: { ...component, protected: true } })).toBe(false);
    expect(canTogglePlacement(host, { ...rows[0], definition: undefined })).toBe(false);
    expect(canTogglePlacement({ ...host, status: "disabled" }, rows[0])).toBe(false);
    expect(
      placementRows({ ...host, status: "disabled" }, [rows[0].placement], [component], {
        components: [{ host_id: "a", component_id: "access", status: "healthy" }]
      })[0].status
    ).toBe("disabled");
    expect(componentPorts(component)).toEqual([11011, 11012]);
  });
  it("uses server observation time for heartbeat age and keeps never-reported and conflict distinct", () => {
    expect(gatewayLabel(snapshot)).toBe("在线");
    expect(gatewayLabel({ ...snapshot, compiled_at: "2026-10-09T12:02:01Z" })).toBe("离线");
    expect(gatewayLabel({ ...snapshot, gateway_status: {} })).toBe("从未上报");
    expect(gatewayLabel({ ...snapshot, gateway_status: { conflict_instance_id: "other" } })).toBe("重复实例");
    expect(gatewayLabel()).toBe("状态未知");
  });
  it("shows persisted pending time only for the same expected and applied snapshot pair", () => {
    const signal = { kind: "route_sync", expected_hash: "new", applied_hash: "old", pending_since: "2026-10-09T11:59:00Z" };
    expect(routeSync(snapshot, signal)).toEqual({ label: "待同步", pendingSince: signal.pending_since });
    expect(routeSync(snapshot, { ...signal, expected_hash: "retired" }).pendingSince).toBe("");
    expect(routeSync(snapshot, { ...signal, applied_hash: "different" }).pendingSince).toBe("");
    expect(routeSync({ ...snapshot, gateway_status: { expected_hash: "new", applied_hash: "new" } }, signal)).toEqual({
      label: "已同步",
      pendingSince: ""
    });
  });
  it("filters callers and methods on the same route and preserves each method ACL", () => {
    const base = {
      component_id: "access",
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
