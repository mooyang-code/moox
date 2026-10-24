<template>
  <div class="topology">
    <div class="filters">
      <span class="filter-label">连线</span>
      <a-checkbox
        v-for="item in EDGE_CATEGORIES"
        :key="item.key"
        :model-value="categories.includes(item.key)"
        @update:model-value="toggleCategory(item.key, $event)"
        ><i class="swatch" :class="item.key" />{{ item.label }} {{ edgeCount(item.key) }}</a-checkbox
      >
      <a-checkbox v-model="showDisabled">显示已停用</a-checkbox>
    </div>
    <div class="legend">
      <span
        ><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" /></svg>本机调用（经本机网关 :11002）</span
      >
      <span
        ><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" stroke-dasharray="6 4" /></svg>跨主机（网关 :11003
        TLS）</span
      >
      <span
        ><svg width="28" height="8"><line x1="0" y1="4" x2="28" y2="4" stroke-dasharray="2 4" /></svg>经控制主机中转</span
      >
      <span v-for="item in STATUS_LEGEND" :key="item.status" class="state" :class="item.status"><i />{{ item.label }}</span>
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
            <i class="swatch" :class="item.edge.category" /><strong>{{ item.node.name }}</strong>
            <span class="muted"
              >{{ item.node.hostId || "外部" }} · {{ serviceNames(item.edge) }} · {{ ROUTE_LABELS[item.edge.route] }}</span
            >
          </p>
          <p v-if="!outgoing.length" class="muted">目录中没有登记它调用的服务</p>
        </section>
        <section>
          <h4>被调用 {{ incoming.length }}</h4>
          <p v-for="item in incoming" :key="item.edge.id">
            <i class="swatch" :class="item.edge.category" /><strong>{{ item.node.name }}</strong>
            <span class="muted"
              >{{ item.node.hostId || "外部" }} · {{ serviceNames(item.edge) }} ·
              {{ item.edge.via || ROUTE_LABELS[item.edge.route] }}</span
            >
          </p>
          <p v-if="!incoming.length" class="muted">目录中没有登记调用它的组件</p>
        </section>
      </div>
    </div>
    <p v-else class="muted hint">点击组件查看它的上下游；选中后显示全部类别的直接关系。</p>
    <div class="canvas-scroll">
      <div class="canvas" :style="{ width: `${topology.width}px`, height: `${topology.height}px` }" @click.self="selectedId = ''">
        <div class="lane-label" :style="{ top: `${topology.externalsY}px` }">外部调用方</div>
        <div
          v-for="host in topology.hosts"
          :key="host.host.host_id"
          class="host-card"
          :class="{
            control: host.isControl,
            disabled: host.host.status === 'disabled',
            located: host.host.host_id === props.locatedHost
          }"
          :data-host-id="host.host.host_id"
          :style="box(host)"
        >
          <div class="host-head">
            <strong>{{ host.host.host_id }}</strong>
            <a-tag v-if="host.isControl" size="small">控制主机</a-tag>
            <a-tag v-if="host.host.reachability === 'outbound'" size="small">只能出站</a-tag>
            <a-tag v-if="host.host.status === 'disabled'" size="small">已停用</a-tag>
          </div>
          <p>
            {{ host.host.address }} · {{ host.host.region || "未指定地域" }}
            <span class="counts"
              ><b class="ok">{{ host.counts.healthy }}</b> 正常<template v-if="host.counts.abnormal">
                · <b class="bad">{{ host.counts.abnormal }}</b> 异常</template
              ><template v-if="host.counts.other"> · {{ host.counts.other }} 其他</template></span
            >
          </p>
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
              v-for="item in EDGE_CATEGORIES"
              :id="`${uid}-${item.key}`"
              :key="item.key"
              viewBox="0 0 8 8"
              refX="7"
              refY="4"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M 0 0 L 8 4 L 0 8 z" class="arrow" :class="item.key" />
            </marker>
          </defs>
          <path
            v-for="edge in drawnEdges"
            :key="edge.id"
            class="edge"
            :class="[edge.category, edge.route, { active: focus?.edges.has(edge.id), dim: focus && !focus.edges.has(edge.id) }]"
            :d="edge.d"
            :marker-end="`url(#${uid}-${edge.category})`"
          />
        </svg>
        <button
          v-for="node in topology.nodes"
          :key="node.id"
          type="button"
          class="node"
          :class="[node.kind, node.status, { selected: node.id === selectedId, dim: focus && !focus.nodes.has(node.id) }]"
          :style="{ left: `${node.x}px`, top: `${node.y}px`, width: `${NODE_W}px`, height: `${NODE_H}px` }"
          :aria-pressed="node.id === selectedId"
          :aria-label="`${node.hostId ? `${node.hostId} ` : ''}${node.name}${node.status ? ` ${statusLabel(node.status)}` : ''}`"
          :title="`${node.name} · ${node.sub}`"
          @mouseenter="hoveredId = node.id"
          @mouseleave="hoveredId = ''"
          @click="selectedId = selectedId === node.id ? '' : node.id"
        >
          <strong>{{ node.name }}</strong
          ><small>{{ node.sub }}</small>
        </button>
      </div>
    </div>
    <p class="muted footnote">
      关系依据组件目录的逐方法 ACL 与外部调用方（principals）推导；消息总线的事件订阅未在目录登记，图中不画。
      <template v-if="topology.unplaced.length">目录中未部署：{{ topology.unplaced.join("、") }}。</template>
    </p>
  </div>
</template>
<script setup lang="ts">
import { computed, ref, useId, watch } from "vue";
import type { ComponentCatalog, DeploymentHost } from "@/api/admin/types";
import { statusColor, statusLabel } from "@/views/ops/monitor/health-display";
import type { PlacementRow } from "./model";
import {
  EDGE_CATEGORIES,
  NODE_H,
  NODE_W,
  ROUTE_LABELS,
  buildTopology,
  edgePath,
  neighborhood,
  type EdgeCategory,
  type TopologyEdge,
  type TopologyHost
} from "./topology";

const props = defineProps<{
  catalog?: ComponentCatalog;
  groups: { host: DeploymentHost; rows: PlacementRow[] }[];
  controlHostId: string;
  /** 链接里 host_id 定位的主机，在拓扑中描边高亮 */
  locatedHost?: string;
}>();
const emit = defineEmits<{ detail: [key: string] }>();
const STATUS_LEGEND = [
  { status: "healthy", label: "正常" },
  { status: "degraded", label: "需关注" },
  { status: "down", label: "异常" },
  { status: "unknown", label: "未知 / 已停用" }
];
const uid = `topology-${useId()}`;
const categories = ref<EdgeCategory[]>(["business", "entry"]);
const showDisabled = ref(false);
const selectedId = ref("");
const hoveredId = ref("");
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
// 聚焦时显示该节点全部类别的直接关系，其余按连线类别筛选。
const drawnEdges = computed(() =>
  topology.value.edges
    .filter(edge => categories.value.includes(edge.category) || focus.value?.edges.has(edge.id))
    .flatMap(edge => {
      const from = nodeById.value.get(edge.from);
      const to = nodeById.value.get(edge.to);
      return from && to ? [{ ...edge, d: edgePath(from, to) }] : [];
    })
);
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
function box(host: TopologyHost) {
  return { left: `${host.x}px`, top: `${host.y}px`, width: `${host.width}px`, height: `${host.height}px` };
}
</script>
<style scoped>
.filters,
.legend,
.focus-head {
  display: flex;
  align-items: center;
  gap: var(--moox-space-3);
  flex-wrap: wrap;
}
.filters {
  margin-bottom: var(--moox-space-2);
}
.filter-label {
  color: var(--color-text-3);
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
  stroke: var(--color-text-3);
  stroke-width: 1.5;
}
.swatch {
  display: inline-block;
  width: 10px;
  height: 3px;
  margin-right: 6px;
  vertical-align: middle;
  border-radius: 2px;
}
.swatch.business,
.arrow.business {
  background: rgb(var(--primary-6));
  fill: rgb(var(--primary-6));
}
.swatch.entry,
.arrow.entry {
  background: rgb(var(--green-6));
  fill: rgb(var(--green-6));
}
.swatch.monitor,
.arrow.monitor {
  background: rgb(var(--orange-6));
  fill: rgb(var(--orange-6));
}
.swatch.ops,
.arrow.ops {
  background: rgb(var(--purple-6));
  fill: rgb(var(--purple-6));
}
.state i {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #94a3b8;
}
.state.healthy i {
  background: #16a34a;
}
.state.degraded i {
  background: #d97706;
}
.state.down i {
  background: #dc2626;
}
.muted {
  color: var(--color-text-3);
}
.hint {
  margin: 0 0 var(--moox-space-2);
  font-size: 12px;
}
.focus-bar {
  padding: 12px 16px;
  margin-bottom: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-left: 3px solid rgb(var(--primary-6));
  border-radius: 6px;
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
.relations h4 {
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
.canvas-scroll {
  overflow: auto;
  background: var(--color-fill-1);
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
}
.canvas {
  position: relative;
  margin: 0 auto;
}
.lane-label {
  position: absolute;
  left: 16px;
  width: 64px;
  padding-top: 12px;
  color: var(--color-text-3);
  font-size: 12px;
  line-height: 1.4;
}
.band .lane-label {
  left: 16px;
  top: 0;
}
.band {
  position: absolute;
  right: 16px;
  left: 0;
  border-top: 1px dashed var(--color-border-2);
  pointer-events: none;
}
.host-card {
  position: absolute;
  box-sizing: border-box;
  padding: 10px 12px;
  background: var(--color-bg-2);
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
}
.host-card.control {
  border-top: 3px solid rgb(var(--primary-6));
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
  font-size: 15px;
}
.host-card p {
  margin: 4px 0 0;
  color: var(--color-text-3);
  font-size: 12px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.counts {
  margin-left: 4px;
}
.counts .ok {
  color: #16803c;
}
.counts .bad {
  color: #dc2626;
}
.edges {
  position: absolute;
  inset: 0;
  pointer-events: none;
}
.edge {
  fill: none;
  stroke-width: 1.4;
  opacity: 0.5;
  transition: opacity 0.15s ease;
}
.edge.business {
  stroke: rgb(var(--primary-6));
}
.edge.entry {
  stroke: rgb(var(--green-6));
}
.edge.monitor {
  stroke: rgb(var(--orange-6));
}
.edge.ops {
  stroke: rgb(var(--purple-6));
}
.edge.cross {
  stroke-dasharray: 6 4;
}
.edge.relay {
  stroke-dasharray: 2 4;
}
.edge.active {
  stroke-width: 2;
  opacity: 1;
}
.edge.dim {
  opacity: 0.06;
}
.node {
  position: absolute;
  box-sizing: border-box;
  padding: 5px 8px 5px 10px;
  overflow: hidden;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
  background: var(--color-bg-2);
  border: 1px solid var(--color-border-2);
  border-left: 3px solid #94a3b8;
  border-radius: 6px;
  transition:
    border-color 0.15s ease,
    box-shadow 0.15s ease,
    opacity 0.15s ease;
}
.node strong,
.node small {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.node strong {
  font-size: 13px;
  font-weight: 600;
}
.node small {
  margin-top: 2px;
  color: var(--color-text-3);
  font-family: monospace;
  font-size: 11px;
}
.node.healthy {
  border-left-color: #16a34a;
}
.node.degraded {
  border-left-color: #d97706;
}
.node.down {
  border-left-color: #dc2626;
}
.node.disabled {
  background: var(--color-fill-2);
}
.node.external {
  background: var(--color-fill-2);
  border-style: dashed;
  border-left: 1px dashed var(--color-border-3);
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
  box-shadow: 0 0 0 2px rgba(var(--primary-6), 0.15);
}
.node.dim {
  opacity: 0.35;
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
