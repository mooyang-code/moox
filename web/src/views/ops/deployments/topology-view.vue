<template>
  <div class="topology">
    <div class="toolbar">
      <a-radio-group v-model="mode" type="button" size="small" aria-label="拓扑视图">
        <a-radio value="flow">数据流向</a-radio>
        <a-radio value="calls">调用关系</a-radio>
      </a-radio-group>
      <template v-if="mode === 'calls'">
        <a-checkbox
          v-for="item in EDGE_CATEGORIES"
          :key="item.key"
          :model-value="categories.includes(item.key)"
          @update:model-value="toggleCategory(item.key, $event)"
          >{{ item.label }} {{ edgeCount(item.key) }}</a-checkbox
        >
      </template>
      <a-checkbox v-model="showDisabled">显示已停用</a-checkbox>
    </div>
    <div class="legend">
      <template v-if="mode === 'flow'">
        <span
          ><svg width="28" height="8"><line class="flow data" x1="0" y1="4" x2="28" y2="4" /></svg>数据写入</span
        >
        <span
          ><svg width="28" height="8"><line class="flow event" x1="0" y1="4" x2="28" y2="4" /></svg>事件（经消息总线）</span
        >
        <span
          ><svg width="28" height="8"><line class="flow control" x1="0" y1="4" x2="28" y2="4" /></svg>调度控制</span
        >
      </template>
      <template v-else>
        <span
          ><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" /></svg>本机调用</span
        >
        <span
          ><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" stroke-dasharray="6 4" /></svg>跨主机（网关
          :11003）</span
        >
        <span
          ><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" stroke-dasharray="2 4" /></svg>经控制主机中转</span
        >
      </template>
      <span class="divider" />
      <span v-for="item in STATUS_LEGEND" :key="item.status"><i class="chip" :class="item.status" />{{ item.label }}</span>
    </div>
    <div v-if="selectedNode" class="focus-bar" role="region" aria-label="选中组件的调用关系">
      <div class="focus-head">
        <strong>{{ selectedNode.name }}</strong>
        <span class="muted">{{ selectedNode.hostId ? `${selectedNode.hostId} · ${selectedNode.sub}` : selectedNode.sub }}</span>
        <a-tag v-if="selectedNode.status" :color="statusColor(selectedNode.status)">{{ statusLabel(selectedNode.status) }}</a-tag>
        <span class="focus-actions">
          <a-button v-if="selectedNode.kind === 'component'" size="small" @click="emit('detail', selectedNode.id)"
            >组件详情</a-button
          >
          <a-button size="small" @click="selectedId = ''">取消选中</a-button>
        </span>
      </div>
      <div class="relations">
        <section>
          <h4>调用 {{ outgoing.length }}</h4>
          <p v-for="item in outgoing" :key="item.edge.id">
            <strong>{{ item.node.name }}</strong>
            <span class="muted"
              >{{ item.node.hostId || "外部" }} · {{ serviceNames(item.edge) }} · {{ ROUTE_LABELS[item.edge.route] }}</span
            >
          </p>
          <p v-if="!outgoing.length" class="muted">目录中没有登记它调用的服务</p>
        </section>
        <section>
          <h4>被调用 {{ incoming.length }}</h4>
          <p v-for="item in incoming" :key="item.edge.id">
            <strong>{{ item.node.name }}</strong>
            <span class="muted"
              >{{ item.node.hostId || "外部" }} · {{ serviceNames(item.edge) }} ·
              {{ item.edge.via || ROUTE_LABELS[item.edge.route] }}</span
            >
          </p>
          <p v-if="!incoming.length" class="muted">目录中没有登记调用它的组件</p>
        </section>
      </div>
    </div>
    <div v-else-if="mode === 'flow'" class="flow-steps" aria-label="数据流向步骤">
      <section v-for="stage in flowStages" :key="stage.name">
        <h4>{{ stage.name }}</h4>
        <p
          v-for="edge in stage.edges"
          :key="edge.id"
          :class="{ hot: hoveredStep === edge.step }"
          @mouseenter="hoveredStep = edge.step"
          @mouseleave="hoveredStep = 0"
        >
          <b class="step">{{ edge.step }}</b
          ><span
            >{{ nodeById.get(edge.from)?.name }} → {{ nodeById.get(edge.to)?.name }}<small>{{ edge.label }}</small></span
          >
        </p>
      </section>
    </div>
    <p v-else class="muted hint">点击组件查看它的上下游；选中后显示全部类别的直接关系。</p>
    <div class="canvas-scroll">
      <div class="canvas" :style="{ width: `${topology.width}px`, height: `${topology.height}px` }" @click.self="selectedId = ''">
        <div class="lane-label" :style="{ top: `${topology.externalsY}px` }">外部</div>
        <div
          v-for="host in topology.hosts"
          :key="host.host.host_id"
          class="host-card"
          :class="{ disabled: host.host.status === 'disabled', located: host.host.host_id === props.locatedHost }"
          :data-host-id="host.host.host_id"
          :style="box(host)"
        >
          <div class="host-head">
            <strong>{{ host.host.host_id }}</strong>
            <a-tag v-if="host.isControl" size="small">控制主机</a-tag>
            <a-tag v-if="host.host.reachability === 'outbound'" size="small">只能出站</a-tag>
            <a-tag v-if="host.host.status === 'disabled'" size="small">已停用</a-tag>
            <span class="host-health">
              <span v-if="host.counts.down" class="down">{{ host.counts.down }} 异常</span>
              <span v-if="host.counts.degraded" class="degraded">{{ host.counts.degraded }} 需关注</span>
              <span v-if="!host.counts.down && !host.counts.degraded && host.counts.healthy" class="healthy">运行正常</span>
            </span>
          </div>
          <p>{{ host.host.address }} · {{ host.host.region || "未指定地域" }}</p>
        </div>
        <div
          v-for="band in topology.bands"
          :key="band.layer"
          class="band"
          :style="{ top: `${band.y}px`, height: `${band.height}px` }"
        >
          <span class="lane-label">{{ band.label }}</span>
        </div>
        <svg class="edges" :width="topology.width" :height="topology.height" aria-hidden="true">
          <defs>
            <marker
              v-for="tone in ['rest', 'active']"
              :id="`${uid}-${tone}`"
              :key="tone"
              viewBox="0 0 8 8"
              refX="7"
              refY="4"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M 0 0 L 8 4 L 0 8 z" class="arrow" :class="tone" />
            </marker>
          </defs>
          <path
            v-for="edge in drawnEdges"
            :key="edge.id"
            class="edge"
            :class="[edge.route, { active: focus?.edges.has(edge.id), dim: focus && !focus.edges.has(edge.id) }]"
            :d="edge.d"
            :marker-end="`url(#${uid}-${focus?.edges.has(edge.id) ? 'active' : 'rest'})`"
          />
          <g
            v-for="edge in drawnFlow"
            :key="edge.id"
            class="flow-edge"
            :class="{ dim: !!focus || (hoveredStep && hoveredStep !== edge.step), hot: hoveredStep === edge.step }"
          >
            <path class="flow" :class="edge.kind" :d="edge.d" :marker-end="`url(#${uid}-active)`" />
          </g>
        </svg>
        <button
          v-for="node in topology.nodes"
          :key="node.id"
          type="button"
          class="node"
          :class="[
            node.kind,
            node.status,
            {
              selected: node.id === selectedId,
              dim: focus ? !focus.nodes.has(node.id) : mode === 'flow' && !flowNodes.has(node.id)
            }
          ]"
          :style="{ left: `${node.x}px`, top: `${node.y}px`, width: `${NODE_W}px`, height: `${NODE_H}px` }"
          :aria-pressed="node.id === selectedId"
          :aria-label="`${node.hostId ? `${node.hostId} ` : ''}${node.name}${node.status ? ` ${statusLabel(node.status)}` : ''}`"
          :title="`${node.name} · ${node.sub}${node.status ? ` · ${statusLabel(node.status)}` : ''}`"
          @mouseenter="hoveredId = node.id"
          @mouseleave="hoveredId = ''"
          @click="selectedId = selectedId === node.id ? '' : node.id"
        >
          <component :is="`icon-${externalIcon(node.id)}`" v-if="node.kind === 'external'" class="ext-icon" /><span>{{
            node.name
          }}</span>
        </button>
        <!-- 步骤编号画在节点之上，避免被节点遮住 -->
        <svg class="edges" :width="topology.width" :height="topology.height" aria-hidden="true">
          <g
            v-for="edge in drawnFlow"
            :key="edge.id"
            class="flow-edge"
            :class="{ dim: !!focus || (hoveredStep && hoveredStep !== edge.step) }"
          >
            <circle :cx="edge.mid.x" :cy="edge.mid.y" r="7.5" class="step-badge" />
            <text :x="edge.mid.x" :y="edge.mid.y" class="step-text">{{ edge.step }}</text>
          </g>
        </svg>
      </div>
    </div>
    <p class="muted footnote">
      <template v-if="mode === 'flow'"
        >数据流向依据总体设计的采集、因子、策略链路，按当前部署落到节点上；未部署的环节自动略过。</template
      >
      <template v-else
        >调用关系依据组件目录的逐方法 ACL 与外部调用方（principals）推导；消息总线的事件订阅见「数据流向」。</template
      >
      <template v-if="topology.unplaced.length">目录中未部署：{{ topology.unplaced.join("、") }}。</template>
    </p>
  </div>
</template>
<script setup lang="ts">
import { computed, ref, useId, watch } from "vue";
import type { ComponentCatalog, DeploymentHost } from "@/api/admin/types";
import { statusColor, statusLabel } from "@/views/ops/monitor/health-display";
import type { DeploymentRow } from "./model";
import {
  EDGE_CATEGORIES,
  NODE_H,
  NODE_W,
  ROUTE_LABELS,
  buildDataFlow,
  buildTopology,
  edgeGeometry,
  neighborhood,
  type EdgeCategory,
  type FlowEdge,
  type TopologyEdge,
  type TopologyHost,
  type TopologyNode
} from "./topology";

const props = defineProps<{
  catalog?: ComponentCatalog;
  groups: { host: DeploymentHost; rows: DeploymentRow[] }[];
  controlHostId: string;
  /** 链接里 host_id 定位的主机，在拓扑中描边高亮 */
  locatedHost?: string;
}>();
const emit = defineEmits<{ detail: [key: string] }>();
const STATUS_LEGEND = [
  { status: "healthy", label: "正常" },
  { status: "degraded", label: "需关注" },
  { status: "down", label: "异常" },
  { status: "unknown", label: "未知" }
];
const EXTERNAL_ICONS: Record<string, string> = {
  "ext:market": "public",
  "ext:console": "desktop",
  "ext:moox-cli": "code-square",
  "ext:scf-collector": "cloud",
  "ext:factor-engine": "computer"
};
const uid = `topology-${useId()}`;
const mode = ref<"flow" | "calls">("flow");
const categories = ref<EdgeCategory[]>(["business", "entry"]);
const showDisabled = ref(false);
const selectedId = ref("");
const hoveredId = ref("");
const hoveredStep = ref(0);
const topology = computed(() =>
  buildTopology({
    catalog: props.catalog,
    groups: props.groups,
    controlHostId: props.controlHostId,
    showDisabled: showDisabled.value
  })
);
const nodeById = computed(() => new Map(topology.value.nodes.map(node => [node.id, node])));
const selectedNode = computed(() => nodeById.value.get(selectedId.value));
// 节点因刷新或筛选消失时同步清掉选中，避免关系面板指向不存在的组件。
watch(nodeById, nodes => {
  if (selectedId.value && !nodes.has(selectedId.value)) selectedId.value = "";
  if (hoveredId.value && !nodes.has(hoveredId.value)) hoveredId.value = "";
});
// 选中优先于悬停：关系面板出现会让画布下移，鼠标落到别的节点上不应改变聚焦。
const focusId = computed(() => selectedId.value || hoveredId.value);
const focus = computed(() => (focusId.value ? neighborhood(topology.value.edges, focusId.value) : undefined));
const flow = computed(() => buildDataFlow(topology.value));
const flowNodes = computed(() => new Set(flow.value.flatMap(edge => [edge.from, edge.to])));
const flowStages = computed(() => {
  const stages = new Map<string, FlowEdge[]>();
  for (const edge of flow.value) stages.set(edge.stage, [...(stages.get(edge.stage) || []), edge]);
  return [...stages].map(([name, edges]) => ({ name, edges }));
});
function withGeometry<T extends { from: string; to: string }>(edges: T[], obstacles: TopologyNode[] = []) {
  return edges.flatMap(edge => {
    const from = nodeById.value.get(edge.from);
    const to = nodeById.value.get(edge.to);
    return from && to ? [{ ...edge, ...edgeGeometry(from, to, obstacles) }] : [];
  });
}
// 调用关系视图按类别筛选；数据流向视图只在聚焦节点时叠加它的直接调用关系。
const drawnEdges = computed(() =>
  withGeometry(
    topology.value.edges.filter(edge =>
      mode.value === "calls"
        ? categories.value.includes(edge.category) || focus.value?.edges.has(edge.id)
        : focus.value?.edges.has(edge.id)
    )
  )
);
const drawnFlow = computed(() => (mode.value === "flow" ? withGeometry(flow.value, topology.value.nodes) : []));
const outgoing = computed(() => relations(edge => edge.from === selectedId.value, "to"));
const incoming = computed(() => relations(edge => edge.to === selectedId.value, "from"));
function relations(match: (edge: TopologyEdge) => boolean, side: "from" | "to") {
  return topology.value.edges
    .filter(match)
    .flatMap(edge => {
      const node = nodeById.value.get(edge[side]);
      return node ? [{ edge, node }] : [];
    })
    .sort((a, b) => a.node.name.localeCompare(b.node.name));
}
function edgeCount(category: EdgeCategory) {
  return topology.value.edges.filter(edge => edge.category === category).length;
}
function toggleCategory(category: EdgeCategory, checked: boolean | (string | number | boolean)[]) {
  categories.value = checked ? [...categories.value, category] : categories.value.filter(item => item !== category);
}
function serviceNames(edge: TopologyEdge) {
  return edge.services.map(path => path.split(".").pop()).join(" / ") || "—";
}
function externalIcon(id: string) {
  return EXTERNAL_ICONS[id] || "user";
}
function box(host: TopologyHost) {
  return { left: `${host.x}px`, top: `${host.y}px`, width: `${host.width}px`, height: `${host.height}px` };
}
</script>
<style scoped>
.toolbar,
.legend,
.focus-head {
  display: flex;
  align-items: center;
  gap: var(--moox-space-3);
  flex-wrap: wrap;
}
.toolbar {
  margin-bottom: var(--moox-space-2);
}
.legend {
  margin-bottom: var(--moox-space-3);
  color: var(--color-text-3);
  font-size: 12px;
}
.legend span {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.legend line {
  stroke: var(--color-neutral-6);
  stroke-width: 1.2;
}
.legend line.flow {
  stroke: rgb(var(--primary-6));
  stroke-width: 2;
}
.legend line.flow.control {
  stroke: rgb(var(--primary-4));
  stroke-width: 1.2;
}
.legend .divider {
  width: 1px;
  height: 12px;
  background: var(--color-border-2);
}
.muted {
  color: var(--color-text-3);
}
.hint {
  margin: 0 0 var(--moox-space-2);
  font-size: 12px;
}
.focus-bar,
.flow-steps {
  padding: 12px 16px;
  margin-bottom: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
}
.focus-actions {
  display: inline-flex;
  gap: var(--moox-space-2);
  margin-left: auto;
}
.relations {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--moox-space-4);
  margin-top: var(--moox-space-2);
}
.relations h4,
.flow-steps h4 {
  margin: 0 0 6px;
  font-size: 13px;
}
.relations p {
  margin: 0;
  padding: 3px 0;
  font-size: 12px;
  overflow-wrap: anywhere;
}
.relations p strong {
  margin-right: 6px;
  font-weight: 500;
}
.flow-steps {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
  gap: var(--moox-space-2) var(--moox-space-4);
}
.flow-steps h4 {
  color: var(--color-text-2);
}
.flow-steps p {
  display: flex;
  gap: 8px;
  margin: 0;
  padding: 3px 4px;
  font-size: 12px;
  line-height: 1.5;
  cursor: default;
  border-radius: 2px;
}
.flow-steps p.hot {
  background: var(--color-fill-2);
}
.flow-steps small {
  display: block;
  color: var(--color-text-3);
}
.step {
  flex: none;
  width: 16px;
  height: 16px;
  margin-top: 1px;
  color: #fff;
  font-size: 11px;
  font-weight: 500;
  line-height: 16px;
  text-align: center;
  background: rgb(var(--primary-6));
  border-radius: 50%;
}
.canvas-scroll {
  overflow: auto;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
}
.canvas {
  position: relative;
  margin: 0 auto;
}
.lane-label {
  position: absolute;
  left: 16px;
  padding-top: 8px;
  color: var(--color-text-3);
  font-size: 12px;
}
.band {
  position: absolute;
  right: 16px;
  left: 0;
  border-top: 1px dashed var(--color-border-2);
  pointer-events: none;
}
.band .lane-label {
  top: 0;
}
.host-card {
  position: absolute;
  box-sizing: border-box;
  padding: 10px 12px;
  background: var(--color-fill-1);
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
}
.host-card.disabled {
  opacity: 0.6;
}
.host-card.located {
  border-color: rgb(var(--primary-6));
  box-shadow: 0 0 0 2px rgba(var(--primary-6), 0.15);
}
.host-head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.host-head strong {
  font-size: 14px;
}
.host-health {
  display: inline-flex;
  gap: 8px;
  margin-left: auto;
  font-size: 12px;
}
.host-health .down {
  color: rgb(var(--danger-6));
}
.host-health .degraded {
  color: rgb(var(--warning-6));
}
.host-health .healthy {
  color: rgb(var(--success-6));
}
.host-card p {
  margin: 4px 0 0;
  overflow: hidden;
  color: var(--color-text-3);
  font-size: 12px;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.edges {
  position: absolute;
  inset: 0;
  pointer-events: none;
}
.edge {
  fill: none;
  stroke: var(--color-neutral-5);
  stroke-width: 1;
  opacity: 0.8;
  transition: opacity 0.15s ease;
}
.edge.cross {
  stroke-dasharray: 6 4;
}
.edge.relay {
  stroke-dasharray: 2 4;
}
.edge.active {
  stroke: rgb(var(--primary-6));
  stroke-width: 1.5;
  opacity: 1;
}
.edge.dim {
  opacity: 0.12;
}
.arrow.rest {
  fill: var(--color-neutral-5);
}
.arrow.active {
  fill: rgb(var(--primary-6));
}
.flow {
  fill: none;
  stroke: rgb(var(--primary-6));
  stroke-width: 2;
}
.flow.event {
  stroke-dasharray: 7 4;
}
.flow.control {
  stroke: rgb(var(--primary-4));
  stroke-width: 1.2;
  stroke-dasharray: 2 3;
}
.flow-edge {
  transition: opacity 0.15s ease;
}
.flow-edge.dim {
  opacity: 0.2;
}
.flow-edge.hot .flow {
  stroke-width: 3;
}
.step-badge {
  fill: rgb(var(--primary-6));
  stroke: var(--color-bg-2);
  stroke-width: 1.5;
}
.step-text {
  fill: #fff;
  font-size: 10px;
  font-weight: 500;
  text-anchor: middle;
  dominant-baseline: central;
}
.node {
  position: absolute;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 0 8px;
  overflow: hidden;
  color: var(--color-text-1);
  font: inherit;
  font-size: 12px;
  white-space: nowrap;
  cursor: pointer;
  background: var(--color-bg-2);
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
  transition:
    border-color 0.15s ease,
    box-shadow 0.15s ease,
    opacity 0.15s ease;
}
.node span {
  overflow: hidden;
  text-overflow: ellipsis;
}
/* 状态底色用主题色的半透明淡染叠在不透明底上：亮暗主题都保持“淡”，且连线不会透出节点 */
.node.healthy,
.chip.healthy {
  background: linear-gradient(rgba(var(--success-6), 0.08), rgba(var(--success-6), 0.08)), var(--color-bg-2);
  border: 1px solid rgba(var(--success-6), 0.35);
}
.node.degraded,
.chip.degraded {
  color: rgb(var(--warning-6));
  background: linear-gradient(rgba(var(--warning-6), 0.1), rgba(var(--warning-6), 0.1)), var(--color-bg-2);
  border: 1px solid rgba(var(--warning-6), 0.7);
}
.node.down,
.chip.down {
  color: rgb(var(--danger-6));
  background: linear-gradient(rgba(var(--danger-6), 0.1), rgba(var(--danger-6), 0.1)), var(--color-bg-2);
  border: 1px solid rgba(var(--danger-6), 0.7);
}
.node.unknown,
.node.unchecked {
  color: var(--color-text-2);
  background: var(--color-fill-2);
  border: 1px dashed var(--color-neutral-5);
}
.node.disabled {
  color: var(--color-text-4);
  background: var(--color-fill-1);
}
/* 外部节点没有边框，名称允许溢出到右侧空白，不截断 */
.node.external,
.node.external span {
  overflow: visible;
}
.node.external {
  justify-content: flex-start;
  color: var(--color-text-2);
  background: transparent;
  border-color: transparent;
}
.ext-icon {
  flex: none;
  color: var(--color-text-3);
  font-size: 14px;
}
.node:hover {
  border-color: rgb(var(--primary-5));
}
.node:focus-visible {
  outline: 2px solid rgb(var(--primary-6));
  outline-offset: 2px;
}
.node.selected {
  border-color: rgb(var(--primary-6));
  box-shadow: 0 0 0 2px rgba(var(--primary-6), 0.2);
}
.node.dim {
  opacity: 0.4;
}
.chip {
  display: inline-block;
  width: 14px;
  height: 10px;
  background: var(--color-fill-2);
  border: 1px dashed var(--color-neutral-5);
  border-radius: 2px;
}
.footnote {
  margin: var(--moox-space-2) 0 0;
  font-size: 12px;
  line-height: 1.6;
}
@media (width <= 768px) {
  .relations {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
