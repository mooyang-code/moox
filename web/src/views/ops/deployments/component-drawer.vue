<template>
  <a-drawer
    :visible="visible"
    :title="row ? `${row.name} @ ${row.hostId}` : '组件详情'"
    width="min(720px, 100vw)"
    :footer="false"
    unmount-on-close
    @cancel="emit('update:visible', false)"
  >
    <template v-if="row">
      <div class="drawer-status">
        <a-tag :color="statusColor(row.healthStatus)">{{ statusLabel(row.healthStatus) }}</a-tag>
        <span>{{ row.healthReason }}</span>
        <a-link @click="openMonitor">在监控告警中查看</a-link>
      </div>

      <h4 class="drawer-heading">组件目录</h4>
      <a-descriptions :column="1" size="small" bordered>
        <a-descriptions-item label="组件">{{ row.name }}（{{ row.componentId }}）</a-descriptions-item>
        <a-descriptions-item label="二进制">{{ row.catalog?.binary || "—" }}</a-descriptions-item>
        <a-descriptions-item label="部署范围">{{ scopeText(row.catalog?.scope) }}</a-descriptions-item>
        <a-descriptions-item label="副本">{{ replicasText(row.catalog?.replicas) }}</a-descriptions-item>
        <a-descriptions-item label="健康检查">{{ healthText }}</a-descriptions-item>
        <a-descriptions-item v-if="row.catalog?.ports?.length" label="其他端口">
          <span v-for="port in row.catalog.ports" :key="port.port" class="port-item">{{ port.name }} :{{ port.port }}</span>
        </a-descriptions-item>
      </a-descriptions>

      <template v-if="acl.length">
        <h4 class="drawer-heading">tRPC 服务与调用方</h4>
        <a-table
          :data="acl"
          :pagination="false"
          size="small"
          row-key="path"
          :bordered="{ cell: true }"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="服务" :width="260">
              <template #cell="{ record }">
                <div>{{ record.path }}</div>
                <div class="muted">端口 {{ record.port }}</div>
              </template>
            </a-table-column>
            <a-table-column title="方法" :width="100">
              <template #cell="{ record }">{{ record.methods }}（只读 {{ record.readOnly }}）</template>
            </a-table-column>
            <a-table-column title="调用方">
              <template #cell="{ record }">
                <a-tag v-for="caller in record.callers" :key="caller" size="small" class="caller-tag">{{ caller }}</a-tag>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </template>

      <h4 class="drawer-heading">部署</h4>
      <a-descriptions :column="1" size="small" bordered>
        <a-descriptions-item label="主机">{{ row.hostId }}</a-descriptions-item>
        <a-descriptions-item label="状态">{{ row.enabled ? "已启用" : "已停用" }}</a-descriptions-item>
        <a-descriptions-item label="来源">
          {{ row.hostComponent ? "主机组件，每台主机自动部署" : "moox.toml 的部署表" }}
        </a-descriptions-item>
        <a-descriptions-item v-if="row.protected" label="保护">受保护，不能停用</a-descriptions-item>
        <a-descriptions-item label="登记时间">{{ formatTime(row.placement.created_at) }}</a-descriptions-item>
        <a-descriptions-item label="更新时间">{{ formatTime(row.placement.updated_at) }}</a-descriptions-item>
      </a-descriptions>

      <template v-if="row.health">
        <h4 class="drawer-heading">运行状态</h4>
        <a-descriptions :column="1" size="small" bordered>
          <a-descriptions-item label="探测地址">{{ row.health.probe?.url || "—" }}</a-descriptions-item>
          <a-descriptions-item label="最近探测">{{ formatTime(row.health.probe?.checked_at) }}</a-descriptions-item>
          <a-descriptions-item v-if="row.health.probe?.raw_error" label="原始错误">
            <span class="raw-error">{{ row.health.probe.raw_error }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="上报实例">{{ row.health.reporter?.instance_id || "—" }}</a-descriptions-item>
          <a-descriptions-item label="版本">{{ row.health.reporter?.version || "—" }}</a-descriptions-item>
          <a-descriptions-item label="最近上报">{{ formatTime(row.health.reporter?.last_seen_at) }}</a-descriptions-item>
        </a-descriptions>
      </template>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useRouter } from "vue-router";
import { formatTime, statusColor, statusLabel } from "@/views/ops/monitor/monitor-display";
import { aclSummary, type DeploymentRow } from "./deployment-view";

const props = defineProps<{ visible: boolean; row: DeploymentRow | null }>();
const emit = defineEmits<{ "update:visible": [value: boolean] }>();
const router = useRouter();

const acl = computed(() => aclSummary(props.row?.catalog));
const healthText = computed(() => {
  const catalog = props.row?.catalog;
  switch (catalog?.health_kind) {
    case "readyz":
      return `/readyz（带 health 签名），端口 ${catalog.health_port}`;
    case "https":
      return "HTTPS 请求控制台入口";
    default:
      return "不探测";
  }
});

function scopeText(scope?: string) {
  switch (scope) {
    case "host":
      return "每台主机";
    case "control":
      return "只能部署在 control";
    case "any":
      return "任意主机";
    default:
      return "—";
  }
}

function replicasText(replicas?: string) {
  switch (replicas) {
    case "per_host":
      return "每台主机一个";
    case "single":
      return "全局只有一个";
    case "multi":
      return "可以部署多份";
    default:
      return "—";
  }
}

function openMonitor() {
  emit("update:visible", false);
  void router.push("/ops/monitor");
}
</script>

<style scoped lang="scss">
.drawer-status {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-3);
}

.drawer-heading {
  margin: var(--moox-space-4) 0 var(--moox-space-2);
}

.muted {
  color: var(--color-text-3);
}

.port-item {
  margin-right: var(--moox-space-3);
}

.caller-tag {
  margin: 2px 4px 2px 0;
}

.raw-error {
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
