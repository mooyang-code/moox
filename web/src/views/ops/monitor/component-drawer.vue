<template>
  <a-drawer
    :visible="visible"
    :title="component ? `${component.name || component.component_id} @ ${component.host_id}` : '组件详情'"
    width="min(720px, 100vw)"
    :footer="false"
    unmount-on-close
    @cancel="emit('update:visible', false)"
  >
    <template v-if="component">
      <div class="drawer-status">
        <a-tag :color="statusColor(component.status)">{{ statusLabel(component.status) }}</a-tag>
        <span class="drawer-status__reason">{{ component.reason }}</span>
        <small v-if="since">已持续 {{ since }}（自 {{ formatTime(component.status_since) }}）</small>
      </div>
      <RawError :text="component.probe?.raw_error" />

      <a-descriptions class="drawer-section" :column="1" size="small" bordered>
        <a-descriptions-item label="组件">{{ component.name }}（{{ component.component_id }}）</a-descriptions-item>
        <a-descriptions-item label="主机">{{ component.host_id }}</a-descriptions-item>
        <a-descriptions-item label="探测地址">{{ probeAddress }}</a-descriptions-item>
        <a-descriptions-item label="运行指标上报">{{ reporterLabel(component.reporter?.status) }}</a-descriptions-item>
        <a-descriptions-item v-if="component.reporter?.instance_id" label="上报实例">
          {{ component.reporter.instance_id }}
        </a-descriptions-item>
        <a-descriptions-item v-if="component.reporter?.version" label="版本">{{
          component.reporter.version
        }}</a-descriptions-item>
        <a-descriptions-item v-if="component.reporter?.last_seen_at" label="最近上报">
          {{ formatTime(component.reporter.last_seen_at) }}
        </a-descriptions-item>
      </a-descriptions>

      <template v-if="component.probe?.status !== 'unchecked' && component.status !== 'disabled'">
        <h4 class="drawer-heading">最近 {{ history.length }} 次探测</h4>
        <a-table
          :data="history"
          :pagination="false"
          size="small"
          row-key="checked_at"
          :bordered="{ cell: true }"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="时间" :width="170">
              <template #cell="{ record }">{{ formatTime(record.checked_at) }}</template>
            </a-table-column>
            <a-table-column title="结果" :width="90">
              <template #cell="{ record }">
                <a-tag size="small" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="原始错误">
              <template #cell="{ record }">
                <span class="probe-error">{{ record.raw_error || "—" }}</span>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </template>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { HealthComponent } from "@/api/monitor";
import RawError from "./raw-error.vue";
import { formatSince, formatTime, reporterLabel, statusColor, statusLabel } from "./monitor-display";

const props = defineProps<{ visible: boolean; component: HealthComponent | null; now: Date }>();
const emit = defineEmits<{ (event: "update:visible", value: boolean): void }>();

const history = computed(() => props.component?.history || []);
const since = computed(() => formatSince(props.component?.status_since, props.now));
const probeAddress = computed(() => {
  if (props.component?.probe?.status === "unchecked") return "不探测";
  return props.component?.probe?.url || "—";
});
</script>

<style scoped lang="scss">
.drawer-status {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
}

.drawer-status__reason {
  color: var(--color-text-1);
}

.drawer-status small {
  color: var(--color-text-3);
}

.drawer-section {
  margin-top: var(--moox-space-4);
}

.drawer-heading {
  margin: var(--moox-space-4) 0 var(--moox-space-2);
}

.probe-error {
  color: var(--color-text-2);
  font-size: 12px;
  word-break: break-all;
}
</style>
