import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { parse } from "yaml";
import type { ComponentCatalog, ComponentPlacement, DeploymentHost } from "@/api/admin/types";
import { placementKey, placementRows } from "./model";
import {
  DATA_FLOW,
  NODE_H,
  NODE_W,
  buildDataFlow,
  buildTopology,
  edgeGeometry,
  layerOf,
  neighborhood,
  type TopologyEdge
} from "./topology";

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
    // 数据流向经过的外部节点排在最前，彼此相邻。
    expect(ids.slice(0, 5)).toEqual(["ext:market", "ext:scf-collector", "ext:factor-engine", "ext:console", "ext:moox-cli"]);
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

  it("lays the collection data flow onto placed nodes and skips steps whose components are not deployed", () => {
    const topology = build();
    const flow = buildDataFlow(topology);
    expect(flow.map(item => item.step)).toEqual(flow.map((_, index) => index + 1));
    expect(flow.map(item => [item.from, item.to])).toEqual([
      [key("control", "collector"), key("control", "cloudfunction")],
      [key("control", "cloudfunction"), "ext:scf-collector"],
      ["ext:market", "ext:scf-collector"],
      // 多副本的 access 取与存储主服务同主机的那一台。
      ["ext:scf-collector", key("storage", "access")],
      [key("storage", "access"), key("storage", "storage-primary")],
      ["ext:market", key("overseas", "collector-stream")],
      [key("overseas", "collector-stream"), key("storage", "storage-primary")],
      [key("storage", "storage-primary"), key("storage", "storage-node")],
      [key("storage", "storage-node"), key("storage", "storage-view")],
      [key("storage", "storage-node"), "ext:factor-engine"],
      ["ext:factor-engine", key("storage", "access")],
      [key("storage", "storage-view"), key("control", "strategy")],
      [key("control", "strategy"), key("overseas", "trade")]
    ]);
    const withoutStream = build(placements.filter(item => item.component_id !== "collector-stream"));
    const steps = buildDataFlow(withoutStream);
    expect(steps).toHaveLength(11);
    expect(steps.map(item => item.step)).toEqual(steps.map((_, index) => index + 1));
  });

  it("only references components and external callers that the catalog still declares", () => {
    const externals = new Set(
      build()
        .nodes.filter(node => node.kind === "external")
        .map(node => node.id)
    );
    const components = new Set(catalog.components.map(item => item.id));
    for (const step of DATA_FLOW)
      for (const ref of [step.from, step.to, step.near].filter(Boolean) as string[])
        expect(ref.startsWith("ext:") ? externals.has(ref) : components.has(ref), ref).toBe(true);
  });

  it("draws vertical curves across rows and horizontal curves within a row with a midpoint for step badges", () => {
    const a = { id: "a", kind: "component" as const, name: "a", sub: "a", x: 0, y: 0 };
    const b = { ...a, id: "b", y: 200 };
    const vertical = edgeGeometry(a, b);
    expect(vertical.d).toMatch(new RegExp(`^M ${NODE_W / 2} ${NODE_H} C`));
    expect(vertical.mid).toEqual({ x: NODE_W / 2, y: (NODE_H + 200) / 2 });
    const horizontal = edgeGeometry(a, { ...a, id: "c", x: 300 });
    expect(horizontal.d).toMatch(new RegExp(`^M ${NODE_W} ${NODE_H / 2} C`));
    expect(horizontal.mid).toEqual({ x: (NODE_W + 300) / 2, y: NODE_H / 2 });
    // 中点压在其他节点上时，编号沿曲线挪到空白处。
    const blocker = { ...a, id: "blocker", y: (NODE_H + 200) / 2 - NODE_H / 2 };
    const moved = edgeGeometry(a, b, [a, b, blocker]).mid;
    expect(moved.y < blocker.y - 10 || moved.y > blocker.y + NODE_H + 10).toBe(true);
  });
});
