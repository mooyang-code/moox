<template>
  <section class="worker-line" aria-label="归档执行器">
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
      <div v-else class="line">
        <span>
          归档执行器
          <span v-if="worker.active" class="is-ok">在线</span>
          <span v-else class="is-bad" :title="formatInstant(worker.last_heartbeat_at, 'UTC')"
            >离线（最后心跳 {{ relativeTime(worker.last_heartbeat_at, nowMs) }}）</span
          >
        </span>
        <span v-if="hostLabel">所在主机 {{ hostLabel }}</span>
        <span v-if="worker.maintaining" class="is-warn">维护中</span>
        <span v-if="disk?.insufficient" class="is-bad" :title="disk.title">{{ disk.warning }}</span>
        <span>本空间运行中 {{ overview.running_runs?.length || 0 }} · 排队 {{ overview.queued_run_count || 0 }}</span>
        <router-link to="/archive/storage">存放位置与磁盘</router-link>
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
import type { ArchiveTask, GetArchiveOverviewRsp } from "@/api/archive";
import { formatInstant } from "@/utils/time-zone";
import { relativeTime } from "@/views/home/home-model";
import { archiveUnavailableHint, diskStatus, systemAlerts, workerHostText, type WorkerHost } from "../shared/archive-model";

/**
 * 归档任务页顶部的执行器状态：一行文字，在线与否、所在主机，磁盘不足时标出；存放位置、磁盘、避让时段在“归档存储”页。
 * 首次读取失败只显示失败和重试；成功过之后失败保留上次的数据，末尾写“读取失败 · 上次成功”。
 */
const props = defineProps<{
  overview: GetArchiveOverviewRsp | null;
  loaded: boolean;
  error: string;
  /** 最近一次成功读取的时刻（毫秒） */
  lastOkAt: number | null;
  tasks: ArchiveTask[];
  nowMs: number;
  /** 执行器所在主机；读不到时为 null，不显示 */
  host: WorkerHost | null;
}>();
const emit = defineEmits<{ retry: [] }>();

const worker = computed(() => props.overview?.worker);
const alerts = computed(() => systemAlerts(props.overview?.alerts, props.tasks));
const unavailableHint = computed(() => archiveUnavailableHint(props.error));
const disk = computed(() => diskStatus(worker.value));
const hostLabel = computed(() => workerHostText(props.host));
</script>

<style scoped>
.worker-line {
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
