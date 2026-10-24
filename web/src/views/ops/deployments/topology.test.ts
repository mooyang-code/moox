import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { parse } from "yaml";
import type { ComponentCatalog, ComponentPlacement, DeploymentHost } from "@/api/admin/types";
import { placementKey, placementRows } from "./model";
import { NODE_H, NODE_W, buildTopology, edgePath, layerOf, neighborhood, type TopologyEdge } from "./topology";

const catalog = parse(
  readFileSync(path.resolve(__dirname, "../../../../../packages/servicecatalog/catalog.yaml"), "utf8")
) as ComponentCatalog;
const hosts: DeploymentHost[] = [
  { host_id: "storage", address: "203.0.113.11", region: "ap-nanjing", status: "enabled" },
  { host_id: "control", address: "203.0.113.10", region: "ap-guangzhou", status: "enabled" },
  { host_id: "overseas", address: "203.0.113.12", region: "ap-hongkong", status: "enabled", reachability: "outbound" }
];
const layout: Record<string, string[]> = {
  control: ["console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudfunction", "factor-mgr", "strategy"],
  storage: ["storage-primary", "storage-node", "storage-view", "access"],
  overseas: ["trade", "access", "egress-proxy", "collector-stream"]
};
const placements: ComponentPlacement[] = hosts.flatMap(host =>
  [...layout[host.host_id], "host-gateway", "host-agent"].map(component_id => ({
    host_id: host.host_id,
    component_id,
    status: "enabled" as const
  }))
);
function build(items = placements, showDisabled = false) {
  const groups = hosts.map(host => ({ host, rows: placementRows(host, items, catalog.components) }));
  return buildTopology({ catalog, groups, controlHostId: "control", showDisabled });
}
const key = placementKey;
function edge(edges: TopologyEdge[], from: string, to: string) {
  return edges.find(item => item.from === from && item.to === to);
}

describe("service topology", () => {
  it("puts the control host first and aligns every layer across host columns", () => {
    const topology = build();
    expect(topology.hosts.map(item => item.host.host_id)).toEqual(["control", "storage", "overseas"]);
    expect(topology.bands.map(item => item.layer)).toEqual(["entry", "control", "business", "storage", "infra"]);
    const infra = topology.bands.find(item => item.layer === "infra")!;
    const gateways = topology.nodes.filter(node => node.componentId === "host-gateway");
    expect(gateways).toHaveLength(3);
    expect(new Set(gateways.map(node => node.y)).size).toBe(1);
    expect(gateways[0].y).toBeGreaterThanOrEqual(infra.y);
    // 节点不重叠，且都落在画布内。
    const boxes = topology.nodes.map(node => `${node.x},${node.y}`);
    expect(new Set(boxes).size).toBe(boxes.length);
    for (const node of topology.nodes) {
      expect(node.x + NODE_W).toBeLessThanOrEqual(topology.width);
      expect(node.y + NODE_H).toBeLessThanOrEqual(topology.height);
    }
    expect(layerOf("future-component")).toBe("other");
  });

  it("derives component calls from ACLs and marks cross-host and relayed routes", () => {
    const { edges } = build();
    const collectorToPrimary = edge(edges, key("control", "collector"), key("storage", "storage-primary"));
    expect(collectorToPrimary).toMatchObject({ category: "business", route: "cross" });
    expect(collectorToPrimary?.services).toEqual(["trpc.moox.storage.Metadata", "trpc.moox.storage.PrimaryStore"]);
    expect(edge(edges, key("control", "strategy"), key("overseas", "trade"))).toMatchObject({ route: "relay" });
    expect(edge(edges, key("control", "collector"), key("control", "cloudfunction"))).toMatchObject({ route: "local" });
    expect(edge(edges, key("control", "monitor"), key("storage", "host-agent"))).toMatchObject({ category: "monitor" });
    // 每台主机网关以 host-gateway@<主机> 身份拉取 Admin 快照。
    for (const host of hosts)
      expect(edge(edges, key(host.host_id, "host-gateway"), key("control", "admin"))).toMatchObject({ category: "ops" });
    expect(edges.some(item => item.from === item.to)).toBe(false);
  });

  it("links principals and console callers as external sources", () => {
    const topology = build();
    const ids = topology.nodes.filter(node => node.kind === "external").map(node => node.id);
    expect(ids.slice(0, 2)).toEqual(["ext:console", "ext:moox-cli"]);
    expect(ids).toContain("ext:factor-engine");
    // FactorEngine 的 ACL 没有调用方，只能从 principals 推出因子引擎这条链路。
    expect(edge(topology.edges, "ext:factor-engine", key("control", "factor-mgr"))).toMatchObject({
      category: "entry",
      services: ["trpc.moox.factor.FactorEngine"]
    });
    expect(edge(topology.edges, "ext:scf-collector", key("storage", "storage-primary"))?.via).toContain("access");
    expect(edge(topology.edges, "ext:console", key("control", "console-proxy"))).toMatchObject({ category: "entry" });
    expect(edge(topology.edges, key("control", "console-proxy"), key("control", "admin"))).toMatchObject({ category: "entry" });
    expect(edge(topology.edges, "ext:console", key("control", "strategy"))).toMatchObject({ category: "ops" });
    expect(topology.unplaced).toEqual(["数据归档管理服务"]);
  });

  it("hides disabled placements unless asked and keeps neighborhoods to direct links", () => {
    const disabled = placements.map(item =>
      item.host_id === "control" && item.component_id === "strategy" ? { ...item, status: "disabled" as const } : item
    );
    expect(build(disabled).nodes.some(node => node.id === key("control", "strategy"))).toBe(false);
    const shown = build(disabled, true);
    expect(shown.nodes.find(node => node.id === key("control", "strategy"))?.status).toBe("disabled");
    const near = neighborhood(build().edges, key("overseas", "trade"));
    expect(near.nodes.has(key("control", "strategy"))).toBe(true);
    expect(near.nodes.has(key("storage", "storage-node"))).toBe(false);
  });

  it("draws vertical curves across rows and horizontal curves within a row", () => {
    const a = { id: "a", kind: "component" as const, name: "a", sub: "a", x: 0, y: 0 };
    expect(edgePath(a, { ...a, id: "b", y: 200 })).toMatch(/^M 59 44 C/);
    expect(edgePath(a, { ...a, id: "b", x: 300 })).toMatch(/^M 118 22 C/);
  });
});
