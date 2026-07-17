<template>
  <div class="master-detail">
    <aside class="host-rail" aria-label="监控主机列表">
      <button
        v-for="row in rows"
        :key="row.key"
        type="button"
        class="rail-item"
        :class="{ selected: row.key === selectedKey, attention: row.attention }"
        :aria-pressed="row.key === selectedKey"
        @click="$emit('select', row.key)"
      >
        <div>
          <strong>{{ row.displayName }}</strong
          ><span>{{ row.displayAddress }}</span>
        </div>
        <span class="rail-state" :class="row.state">{{ stateText(row) }}</span>
        <dl>
          <div>
            <dt>CPU</dt>
            <dd>{{ percent(row.cpuUsage) }}</dd>
          </div>
          <div>
            <dt>内存</dt>
            <dd>{{ percent(row.memoryUsage) }}</dd>
          </div>
          <div>
            <dt>文件系统</dt>
            <dd>{{ percent(row.filesystemUsage) }}</dd>
          </div>
        </dl>
      </button>
    </aside>
    <div class="detail-pane">
      <HostMonitorDetail :row="selectedRow" :refresh-key="refreshKey" />
    </div>
  </div>
</template>

<script setup lang="ts">
import HostMonitorDetail from "./host-monitor-detail.vue";
import type { HostMonitorRow } from "./host-monitor-mapping";

defineProps<{ rows: HostMonitorRow[]; selectedKey: string; selectedRow: HostMonitorRow | null; refreshKey?: number }>();
defineEmits<{ select: [key: string] }>();

const percent = (value: number | null) => (value === null ? "--" : `${value}%`);
const stateText = (row: HostMonitorRow) => (row.state === "online" ? "在线" : row.state === "offline" ? "离线" : "未接入");
</script>

<style scoped lang="scss">
.master-detail {
  display: grid;
  grid-template-columns: 270px minmax(0, 1fr);
  gap: 18px;
  min-height: 0;
}
.host-rail {
  display: flex;
  flex-direction: column;
  gap: 7px;
  min-height: 0;
  max-height: calc(100vh - 290px);
  padding-right: var(--moox-space-1);
  overflow-y: auto;
}
.rail-item {
  appearance: none;
  width: 100%;
  padding: 11px;
  color: inherit;
  font: inherit;
  text-align: left;
  background: var(--color-bg-2);
  border: 1px solid var(--color-border-2);
  border-radius: 5px;
  cursor: pointer;
}
.rail-item.selected {
  border-color: rgb(var(--primary-6));
  background: rgba(var(--primary-6), 0.06);
}
.rail-item.attention {
  border-left: 3px solid #d97706;
}
.rail-item > div:first-child {
  display: flex;
  justify-content: space-between;
  gap: var(--moox-space-2);
}
.rail-item strong,
.rail-item span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rail-item strong {
  font-size: 13px;
}
.rail-item > div:first-child span {
  color: var(--color-text-3);
  font-size: 11px;
}
.rail-state {
  display: block;
  margin-top: var(--moox-space-1);
  color: var(--color-text-3);
  font-size: 11px;
}
.rail-state.online {
  color: #16803c;
}
.rail-state.offline {
  color: #dc2626;
}
.rail-item dl {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 5px;
  margin: 9px 0 0;
}
.rail-item dl div {
  padding: 5px;
  background: var(--color-fill-1);
}
.rail-item dt {
  color: var(--color-text-3);
  font-size: 10px;
}
.rail-item dd {
  margin: 2px 0 0;
  font-size: 12px;
  font-weight: 600;
}
.detail-pane {
  min-width: 0;
}
.detail-pane :deep(.monitor-detail) {
  padding-top: 0;
  margin-top: 0;
  border-top: 0;
}
@media (max-width: 820px) {
  .master-detail {
    grid-template-columns: minmax(0, 1fr);
  }
  .host-rail {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    max-height: 320px;
  }
  .detail-pane {
    padding-top: var(--moox-space-4);
    border-top: 1px solid var(--color-border-2);
  }
}
@media (max-width: 560px) {
  .host-rail {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
