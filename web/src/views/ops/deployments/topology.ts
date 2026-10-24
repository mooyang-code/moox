import type { ComponentCatalog, DeploymentHost } from "@/api/admin/types";
import type { HealthStatus } from "@/api/monitor";
import type { DeploymentRow } from "./model";

// 服务拓扑：调用关系只依据组件目录（ACL、principals）、主机与放置推导，不凭印象补关系；
// 数据流向是总体设计第 5、6 节的固定链路（事件订阅不在目录里），按实际放置落到节点上。
// 坐标在这里一次算好，HTML 节点和 SVG 连线共用，刷新时不抖动，也能在无 DOM 环境下测试。

export type TopologyLayer = "entry" | "control" | "business" | "storage" | "infra" | "other";
export type EdgeCategory = "business" | "entry" | "monitor" | "ops";
export type EdgeRoute = "local" | "cross" | "relay" | "external";

export const LAYERS: { key: TopologyLayer; label: string }[] = [
  { key: "entry", label: "入口" },
  { key: "control", label: "控制面" },
  { key: "business", label: "业务服务" },
  { key: "storage", label: "数据存储" },
  { key: "other", label: "其他" },
  { key: "infra", label: "主机基础" }
];
export const EDGE_CATEGORIES: { key: EdgeCategory; label: string }[] = [
  { key: "business", label: "业务调用" },
  { key: "entry", label: "外部入口" },
  { key: "monitor", label: "监控巡检" },
  { key: "ops", label: "运维管控" }
];
const LAYER_OF: Record<string, TopologyLayer> = {
  "console-proxy": "entry",
  "web-host": "entry",
  access: "entry",
  admin: "control",
  monitor: "control",
  eventbus: "control",
  collector: "business",
  "collector-stream": "business",
  cloudfunction: "business",
  "factor-mgr": "business",
  "archive-mgr": "business",
  strategy: "business",
  trade: "business",
  "egress-proxy": "business",
  "storage-primary": "storage",
  "storage-node": "storage",
  "storage-view": "storage",
  "host-gateway": "infra",
  "host-agent": "infra"
};
// 外部调用方的展示名；目录里新增的 principal 没有登记时直接显示 ID。
const EXTERNAL_LABELS: Record<string, { name: string; note: string }> = {
  // 数据流向经过的外部节点排在一起，避免流向线横穿其他外部节点。
  market: { name: "行情源", note: "交易所 / 行情接口" },
  "scf-collector": { name: "SCF 采集函数", note: "腾讯云函数" },
  "factor-engine": { name: "因子引擎", note: "操作员机器" },
  console: { name: "管理台", note: "浏览器 · HTTPS 9527" },
  "moox-cli": { name: "moox-cli", note: "运维命令行" },
  "archive-worker": { name: "归档 Worker", note: "外部进程" },
  "collector-operator": { name: "采集运维", note: "外部调用方" },
  "moox-skill": { name: "Skill", note: "外部调用方" }
};
// 管理台入口由 console-proxy 固定分流：静态资源到 web-host，/api/admin/* 到 admin。
const ENTRY_LINKS: { from: string; to: string; label: string }[] = [
  { from: "ext:console", to: "console-proxy", label: "HTTPS :9527" },
  { from: "console-proxy", to: "web-host", label: "静态资源 :9528" },
  { from: "console-proxy", to: "admin", label: "/api/admin/*" }
];

// 数据流向：行情从源头进入存储，再驱动视图、因子、策略与交易。
// kind：data 为同步调用/写入，event 经消息总线投递，control 为调度控制。
// 端点是组件 ID 或 ext:<外部 ID>；near 指定多副本组件（如 access）优先取与哪个组件同主机的实例。
export type FlowKind = "data" | "event" | "control";
export interface FlowStep {
  from: string;
  to: string;
  kind: FlowKind;
  stage: string;
  label: string;
  near?: string;
}
export const DATA_FLOW: FlowStep[] = [
  { from: "collector", to: "cloudfunction", kind: "control", stage: "采集调度", label: "规划采集计划，下发函数配置" },
  { from: "cloudfunction", to: "ext:scf-collector", kind: "control", stage: "采集调度", label: "Timer / Invoke 触发采集" },
  { from: "ext:market", to: "ext:scf-collector", kind: "data", stage: "SCF 采集", label: "拉取 K 线等行情" },
  { from: "ext:scf-collector", to: "access", kind: "data", stage: "SCF 采集", label: "签名写入 :11004", near: "storage-primary" },
  {
    from: "access",
    to: "storage-primary",
    kind: "data",
    stage: "SCF 采集",
    label: "CommitTimeSeriesBatch 写入 K 线",
    near: "storage-primary"
  },
  { from: "ext:market", to: "collector-stream", kind: "data", stage: "WS 推送", label: "交易所 WS 实时推送" },
  { from: "collector-stream", to: "storage-primary", kind: "data", stage: "WS 推送", label: "写入 K 线" },
  { from: "storage-primary", to: "storage-node", kind: "data", stage: "存储", label: "写入事实存储，到点固化采集周期" },
  { from: "storage-node", to: "storage-view", kind: "event", stage: "存储", label: "行变更与周期标记，构建 View" },
  { from: "storage-node", to: "ext:factor-engine", kind: "event", stage: "因子", label: "采集周期完成，触发因子计算" },
  { from: "ext:factor-engine", to: "access", kind: "data", stage: "因子", label: "写回因子结果", near: "storage-primary" },
  { from: "storage-view", to: "strategy", kind: "event", stage: "策略交易", label: "View 数据就绪，计算目标权重" },
  { from: "strategy", to: "trade", kind: "event", stage: "策略交易", label: "目标权重请求，收敛下单" }
];

export const NODE_W = 112;
export const NODE_H = 30;
const NODE_GAP = 8;
// 横向间距留出步骤编号的位置。
const GAP_X = 20;
const CELL_PAD = 12;
const COL_W = CELL_PAD * 2 + NODE_W * 2 + GAP_X;
const COL_GAP = 20;
const PAD = 16;
const LABEL_W = 72;
const HOST_HEAD_H = 60;
const BAND_GAP = 48;

export interface TopologyNode {
  id: string;
  kind: "component" | "external";
  name: string;
  sub: string;
  hostId?: string;
  componentId?: string;
  layer?: TopologyLayer;
  status?: HealthStatus;
  x: number;
  y: number;
}
export interface TopologyEdge {
  id: string;
  from: string;
  to: string;
  category: EdgeCategory;
  route: EdgeRoute;
  services: string[];
  via: string;
}
export interface TopologyHost {
  host: DeploymentHost;
  isControl: boolean;
  counts: Record<"healthy" | "degraded" | "down" | "other", number>;
  x: number;
  y: number;
  width: number;
  height: number;
}
export interface TopologyBand {
  layer: TopologyLayer;
  label: string;
  y: number;
  height: number;
}
export interface Topology {
  nodes: TopologyNode[];
  edges: TopologyEdge[];
  hosts: TopologyHost[];
  bands: TopologyBand[];
  externalsY: number;
  width: number;
  height: number;
  unplaced: string[];
}
export interface TopologyInput {
  catalog?: ComponentCatalog;
  groups: { host: DeploymentHost; rows: DeploymentRow[] }[];
  controlHostId: string;
  showDisabled: boolean;
}

export function layerOf(componentId: string): TopologyLayer {
  return LAYER_OF[componentId] || "other";
}

export function buildTopology(input: TopologyInput): Topology {
  const components = input.catalog?.components || [];
  const groups = input.groups
    .filter(group => input.showDisabled || group.host.status === "enabled")
    .map(group => ({ ...group, rows: group.rows.filter(row => input.showDisabled || row.status !== "disabled") }))
    .sort((a, b) => Number(b.host.host_id === input.controlHostId) - Number(a.host.host_id === input.controlHostId));

  // 1. 按“主机列 × 分层行”排布组件；各层行高取所有主机里最高的一格，保证层次横向对齐。
  const cells = groups.map(group => {
    const byLayer = new Map<TopologyLayer, DeploymentRow[]>();
    for (const row of group.rows) {
      const layer = layerOf(row.deployment.component_id);
      byLayer.set(layer, [...(byLayer.get(layer) || []), row]);
    }
    return byLayer;
  });
  const rowsOf = (count: number) => Math.ceil(count / 2);
  const presentLayers = LAYERS.filter(layer => cells.some(cell => cell.has(layer.key)));
  const externals = collectExternals(input.catalog);
  const columnsX = groups.map((_, index) => PAD + LABEL_W + index * (COL_W + COL_GAP));
  const contentRight = groups.length ? columnsX[columnsX.length - 1] + COL_W : PAD + LABEL_W + COL_W;
  const perLine = Math.max(1, Math.floor((contentRight - PAD - LABEL_W + GAP_X) / (NODE_W + GAP_X)));
  const externalsY = PAD;
  const externalLines = Math.max(1, Math.ceil(externals.length / perLine));
  const hostsTop = externalsY + externalLines * (NODE_H + NODE_GAP) - NODE_GAP + BAND_GAP;

  const nodes: TopologyNode[] = externals.map((item, index) => ({
    id: `ext:${item.id}`,
    kind: "external",
    name: item.name,
    sub: item.note,
    x: PAD + LABEL_W + (index % perLine) * (NODE_W + GAP_X),
    y: externalsY + Math.floor(index / perLine) * (NODE_H + NODE_GAP)
  }));
  const bands: TopologyBand[] = [];
  let y = hostsTop + HOST_HEAD_H;
  for (const layer of presentLayers) {
    const lines = Math.max(...cells.map(cell => rowsOf(cell.get(layer.key)?.length || 0)));
    const height = CELL_PAD * 2 + lines * (NODE_H + NODE_GAP) - NODE_GAP;
    bands.push({ layer: layer.key, label: layer.label, y, height });
    cells.forEach((cell, column) => {
      (cell.get(layer.key) || []).forEach((row, index) => {
        nodes.push({
          id: row.key,
          kind: "component",
          name: row.definition?.name || row.deployment.component_id,
          sub: row.deployment.component_id,
          hostId: row.deployment.host_id,
          componentId: row.deployment.component_id,
          layer: layer.key,
          status: row.status,
          x: columnsX[column] + CELL_PAD + (index % 2) * (NODE_W + GAP_X),
          y: y + CELL_PAD + Math.floor(index / 2) * (NODE_H + NODE_GAP)
        });
      });
    });
    y += height;
  }
  const hostsBottom = y + CELL_PAD;
  const hosts: TopologyHost[] = groups.map((group, column) => ({
    host: group.host,
    isControl: group.host.host_id === input.controlHostId,
    counts: {
      healthy: group.rows.filter(row => row.status === "healthy").length,
      degraded: group.rows.filter(row => row.status === "degraded").length,
      down: group.rows.filter(row => row.status === "down").length,
      other: group.rows.filter(row => !["healthy", "down", "degraded"].includes(row.status)).length
    },
    x: columnsX[column],
    y: hostsTop,
    width: COL_W,
    height: hostsBottom - hostsTop
  }));

  // 2. 由 ACL 与 principals 推导调用关系；同一对节点的多个服务合并成一条边。
  const componentIds = new Set(components.map(item => item.id));
  const owner = new Map<string, string>();
  for (const component of components) for (const service of component.services || []) owner.set(service.path, component.id);
  const placed = new Map<string, TopologyNode[]>();
  for (const node of nodes) if (node.componentId) placed.set(node.componentId, [...(placed.get(node.componentId) || []), node]);
  const nodeById = new Map(nodes.map(node => [node.id, node]));
  const hostById = new Map(groups.map(group => [group.host.host_id, group.host]));
  const edges = new Map<string, TopologyEdge>();
  const link = (from: TopologyNode, to: TopologyNode, category: EdgeCategory, service: string, via = "") => {
    if (from.id === to.id) return;
    const id = `${from.id}->${to.id}|${category}`;
    const edge = edges.get(id) || {
      id,
      from: from.id,
      to: to.id,
      category,
      route: routeOf(from, to, hostById),
      services: [],
      via
    };
    if (service && !edge.services.includes(service)) edge.services.push(service);
    edges.set(id, edge);
  };
  for (const component of components) {
    for (const service of component.services || []) {
      const targets = placed.get(component.id) || [];
      const callers = [...new Set(service.acl.flatMap(grant => grant.callers))];
      for (const caller of callers) {
        const sources = sourcesOf(caller, placed, nodeById);
        const category = categoryOf(caller, componentIds.has(caller));
        for (const target of targets) for (const source of sources) link(source, target, category, service.path, viaOf(caller));
      }
    }
  }
  for (const principal of input.catalog?.principals || []) {
    const source = nodeById.get(`ext:${principal.id}`);
    if (!source) continue;
    for (const grant of principal.allow) {
      const componentId = owner.get(grant.service);
      for (const target of (componentId && placed.get(componentId)) || [])
        link(source, target, "entry", grant.service, "经外部接入 access :11004 转发");
    }
  }
  for (const entry of ENTRY_LINKS) {
    const sources = entry.from.startsWith("ext:") ? [nodeById.get(entry.from)].filter(isNode) : placed.get(entry.from) || [];
    for (const source of sources)
      for (const target of placed.get(entry.to) || [])
        if (!source.hostId || source.hostId === target.hostId) link(source, target, "entry", entry.label);
  }

  const unplacedIds = new Set(components.map(item => item.id));
  for (const group of input.groups) for (const row of group.rows) unplacedIds.delete(row.deployment.component_id);
  return {
    nodes,
    edges: [...edges.values()],
    hosts,
    bands,
    externalsY,
    width: contentRight + PAD,
    height: hostsBottom + PAD,
    unplaced: components.filter(item => unplacedIds.has(item.id)).map(item => item.name || item.id)
  };
}

function collectExternals(catalog?: ComponentCatalog) {
  const componentIds = new Set((catalog?.components || []).map(item => item.id));
  const ids = new Set<string>(["market", "console"]);
  for (const component of catalog?.components || [])
    for (const service of component.services || [])
      for (const grant of service.acl)
        for (const caller of grant.callers) if (!componentIds.has(caller) && caller !== "host-gateway@*") ids.add(caller);
  for (const principal of catalog?.principals || []) ids.add(principal.id);
  const order = Object.keys(EXTERNAL_LABELS);
  return [...ids]
    .sort((a, b) => rank(order, a) - rank(order, b) || a.localeCompare(b))
    .map(id => ({ id, ...(EXTERNAL_LABELS[id] || { name: id, note: "外部调用方" }) }));
}
function rank(order: string[], id: string) {
  const index = order.indexOf(id);
  return index < 0 ? order.length : index;
}
function sourcesOf(caller: string, placed: Map<string, TopologyNode[]>, nodeById: Map<string, TopologyNode>) {
  if (caller === "host-gateway@*") return placed.get("host-gateway") || [];
  return placed.get(caller) || [nodeById.get(`ext:${caller}`)].filter(isNode);
}
function categoryOf(caller: string, isComponent: boolean): EdgeCategory {
  if (caller === "monitor") return "monitor";
  if (caller === "console" || caller === "moox-cli" || caller === "host-gateway@*") return "ops";
  return isComponent ? "business" : "entry";
}
function viaOf(caller: string) {
  if (caller === "console") return "经 console-proxy → Admin 以 console 身份转发";
  if (caller === "host-gateway@*") return "主机网关拉取路由快照";
  return "";
}
function routeOf(from: TopologyNode, to: TopologyNode, hosts: Map<string, DeploymentHost>): EdgeRoute {
  if (!from.hostId || !to.hostId) return "external";
  if (from.hostId === to.hostId) return "local";
  // 只能出站的主机收不到入站连接，发往它的请求由控制主机网关经反向连接中转。
  return hosts.get(to.hostId)?.reachability === "outbound" ? "relay" : "cross";
}
function isNode(node: TopologyNode | undefined): node is TopologyNode {
  return !!node;
}

// 选中节点时只保留与它直接相连的边和节点。
export function neighborhood(edges: TopologyEdge[], id: string) {
  const nodes = new Set([id]);
  const related = edges.filter(edge => edge.from === id || edge.to === id);
  for (const edge of related) {
    nodes.add(edge.from);
    nodes.add(edge.to);
  }
  return { nodes, edges: new Set(related.map(edge => edge.id)) };
}

export interface FlowEdge {
  id: string;
  step: number;
  from: string;
  to: string;
  kind: FlowKind;
  stage: string;
  label: string;
}

// 把数据流步骤落到实际节点；端点组件没有部署的步骤直接略过，编号保持连续。
export function buildDataFlow(topology: Topology, steps = DATA_FLOW): FlowEdge[] {
  const placed = new Map<string, TopologyNode[]>();
  for (const node of topology.nodes)
    if (node.componentId) placed.set(node.componentId, [...(placed.get(node.componentId) || []), node]);
  const external = new Map(topology.nodes.filter(node => node.kind === "external").map(node => [node.id, node]));
  const resolve = (ref: string, near?: string) => {
    if (ref.startsWith("ext:")) return external.get(ref);
    const nodes = placed.get(ref) || [];
    const hosts = new Set((near && placed.get(near)?.map(node => node.hostId)) || []);
    return nodes.find(node => hosts.has(node.hostId)) || nodes[0];
  };
  const edges: FlowEdge[] = [];
  for (const step of steps) {
    const from = resolve(step.from, step.near);
    const to = resolve(step.to, step.near);
    if (!from || !to || from.id === to.id) continue;
    edges.push({
      id: `flow:${from.id}->${to.id}`,
      step: edges.length + 1,
      from: from.id,
      to: to.id,
      kind: step.kind,
      stage: step.stage,
      label: step.label
    });
  }
  return edges;
}

// 连线锚点：上下错开走竖向贝塞尔，同一行走横向贝塞尔，同一位置附近绕到右侧；
// mid 用来放数据流的步骤编号：从曲线中点向两端找第一个不压在节点上的位置。
const BADGE_T = [0.5, 0.42, 0.58, 0.34, 0.66, 0.26, 0.74, 0.18, 0.82];
const BADGE_CLEARANCE = 10;
export function edgeGeometry(from: TopologyNode, to: TopologyNode, obstacles: TopologyNode[] = []) {
  const fx = from.x + NODE_W / 2;
  const tx = to.x + NODE_W / 2;
  let points: [number, number][];
  if (Math.abs(from.y - to.y) >= NODE_H) {
    const down = to.y > from.y;
    const sy = down ? from.y + NODE_H : from.y;
    const ty = down ? to.y : to.y + NODE_H;
    const bend = Math.max(24, Math.abs(ty - sy) / 2) * (down ? 1 : -1);
    points = [
      [fx, sy],
      [fx, sy + bend],
      [tx, ty - bend],
      [tx, ty]
    ];
  } else {
    const cy = from.y + NODE_H / 2;
    const ty = to.y + NODE_H / 2;
    if (Math.abs(fx - tx) < NODE_W) {
      const sx = Math.max(from.x, to.x) + NODE_W + 28;
      points = [
        [from.x + NODE_W, cy],
        [sx, cy],
        [sx, ty],
        [to.x + NODE_W, ty]
      ];
    } else {
      const right = tx > fx;
      const sx = right ? from.x + NODE_W : from.x;
      const ex = right ? to.x : to.x + NODE_W;
      const bend = Math.max(24, Math.abs(ex - sx) / 2) * (right ? 1 : -1);
      points = [
        [sx, cy],
        [sx + bend, cy],
        [ex - bend, ty],
        [ex, ty]
      ];
    }
  }
  const [p0, p1, p2, p3] = points;
  const at = (t: number) => {
    const u = 1 - t;
    const [a, b, c, d] = [u * u * u, 3 * u * u * t, 3 * u * t * t, t * t * t];
    return { x: a * p0[0] + b * p1[0] + c * p2[0] + d * p3[0], y: a * p0[1] + b * p1[1] + c * p2[1] + d * p3[1] };
  };
  const covered = (point: { x: number; y: number }) =>
    obstacles.some(
      node =>
        point.x > node.x - BADGE_CLEARANCE &&
        point.x < node.x + NODE_W + BADGE_CLEARANCE &&
        point.y > node.y - BADGE_CLEARANCE &&
        point.y < node.y + NODE_H + BADGE_CLEARANCE
    );
  const mid = BADGE_T.map(at).find(point => !covered(point)) || at(0.5);
  return { d: `M ${p0[0]} ${p0[1]} C ${p1[0]} ${p1[1]}, ${p2[0]} ${p2[1]}, ${p3[0]} ${p3[1]}`, mid };
}

export const ROUTE_LABELS: Record<EdgeRoute, string> = {
  local: "本机 · 经本机网关 :11002",
  cross: "跨主机 · 网关 :11003 TLS",
  relay: "跨主机 · 经控制主机中转（只能出站）",
  external: "外部调用"
};
