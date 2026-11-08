<template>
  <section class="worker-status" aria-label="归档执行器">
    <div v-if="!loaded && !error" class="line muted">读取中…</div>
    <div v-else-if="!loaded && error" class="line is-bad">
      无法读取归档服务：{{ error }}<template v-if="unavailableHint">。{{ unavailableHint }}</template> ·
      <a-link @click="emit('retry')">重试</a-link>
    </div>
    <template v-else-if="overview">
      <div v-if="!worker" class="line is-warn">
        还没有归档执行器登记。在因子机部署 archive-worker：<code
          >moox-cli setup deploy-unit --host &lt;主机&gt; --profile archive-worker</code
        >
      </div>
      <div v-else class="line" :class="{ 'is-bad': !worker.active }">
        <span :title="workerTitle">归档执行器 {{ worker.worker_id }}</span>
        <template v-if="worker.active">
          <span class="is-ok">在线</span>
          <span :title="formatInstant(worker.last_heartbeat_at, 'UTC')"
            >{{ relativeTime(worker.last_heartbeat_at, nowMs) }}心跳</span
          >
        </template>
        <span v-else
          >离线 · 最后心跳 {{ formatInstant(worker.last_heartbeat_at, "UTC") }}（{{
            relativeTime(worker.last_heartbeat_at, nowMs)
          }}）</span
        >
        <span v-if="worker.maintaining" class="is-warn">维护中</span>
        <span>磁盘剩余 {{ formatBytes(toNum(worker.disk_free_bytes)) }} / {{ formatBytes(toNum(worker.disk_total_bytes)) }}</span>
        <span>暂存库 {{ formatBytes(toNum(worker.staging_bytes)) }}</span>
        <span>本空间运行中 {{ overview.running_runs?.length || 0 }} · 排队 {{ overview.queued_run_count || 0 }}</span>
        <span v-if="objectStoreText">{{ objectStoreText }}</span>
      </div>
      <div v-if="staleWorkers.length" class="line muted">
        <span :title="staleTitle">另有 {{ staleWorkers.length }} 个过期的执行器登记</span>，确认不再使用后可以退役：<code
          >moox-cli archive worker retire --worker &lt;ID&gt; --yes</code
        >
      </div>
      <div v-for="alert in alerts" :key="alert" class="line is-warn">{{ alert }}</div>
      <div v-if="error" class="line is-bad">
        读取失败 · 上次成功 {{ lastOkAt ? formatInstant(lastOkAt, "UTC", "clock") : "—" }} ·
        <a-link @click="emit('retry')">重试</a-link>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { GetArchiveOverviewRsp } from "@/api/archive";
import { formatBytes } from "@/api/modules/host-monitor";
import { formatInstant } from "@/utils/time-zone";
import { relativeTime } from "@/views/home/home-model";
import { archiveUnavailableHint, systemAlerts, toNum } from "./archive-model";

/**
 * 执行器状态行：一行文字，不用卡片。执行器和空间无关，时间按 UTC；运行与排队只算当前空间（GetArchiveOverview 按请求头的空间算）。
 * 首次读取失败只显示失败和重试；成功过之后失败保留上次的数据，末尾写“读取失败 · 上次成功”。
 */
const props = defineProps<{
  overview: GetArchiveOverviewRsp | null;
  loaded: boolean;
  error: string;
  /** 最近一次成功读取的时刻（毫秒） */
  lastOkAt: number | null;
  taskIds: string[];
  nowMs: number;
}>();
const emit = defineEmits<{ retry: [] }>();

const worker = computed(() => props.overview?.worker);
const staleWorkers = computed(() => props.overview?.stale_workers || []);
const alerts = computed(() => systemAlerts(props.overview?.alerts, props.taskIds));
const unavailableHint = computed(() => archiveUnavailableHint(props.error));
const workerTitle = computed(() => {
  const w = worker.value;
  return w ? [`目录 ${w.root_dir || "—"}`, `版本 ${w.version || "—"}`, `实例 ${w.instance_id || "—"}`].join("\n") : "";
});
const staleTitle = computed(() =>
  staleWorkers.value.map(item => `${item.worker_id} · 最后心跳 ${formatInstant(item.last_heartbeat_at, "UTC")}`).join("\n")
);
// 批 2 之后才有：worker 报告启用了对象存储时写远端副本的位置
const objectStoreText = computed(() => {
  const store = worker.value?.object_store;
  if (!store?.endpoint) return "";
  const where = [store.endpoint, store.bucket, store.prefix].filter(Boolean).join(" / ");
  const cls = store.storage_class ? ` · ${store.storage_class === "STANDARD_IA" ? "低频" : store.storage_class}` : "";
  return `远端副本 ${where}${cls}`;
});
</script>

<style scoped>
.worker-status {
  display: grid;
  gap: 4px;
  margin-bottom: var(--moox-space-3);
}

.line {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  line-height: 22px;
}

.muted {
  color: var(--color-text-3);
}

.is-ok {
  color: rgb(var(--green-6));
}

.is-warn {
  color: rgb(var(--orange-6));
}

.is-bad {
  color: rgb(var(--red-6));
}

code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
</style>
