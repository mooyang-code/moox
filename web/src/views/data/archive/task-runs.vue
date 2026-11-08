<template>
  <div class="task-runs">
    <div class="filters">
      <a-select
        :model-value="query.status"
        style="width: 140px"
        @change="(value: unknown) => emit('update:query', { status: String(value) as ArchiveQuery['status'], rpage: 1 })"
      >
        <a-option value="">全部状态</a-option>
        <a-option v-for="status in RUN_STATUSES" :key="status" :value="status">{{ runStatusLabel(status) }}</a-option>
      </a-select>
      <a-checkbox
        :model-value="query.ticks"
        @change="(value: unknown) => emit('update:query', { ticks: Boolean(value), rpage: 1 })"
        >显示成功的到点拉取</a-checkbox
      >
      <span class="sub">每分钟一条，默认只看复核与异常</span>
    </div>
    <div v-if="error" class="load-error">
      读取运行记录失败：{{ error }}<template v-if="runs.length">（下面是上次读到的）</template> ·
      <a-link @click="load">重试</a-link>
    </div>
    <a-table
      row-key="run_id"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading && !loaded"
      :data="runs"
      :scroll="{ x: 1500 }"
      :pagination="{ current: query.rpage, pageSize: PAGE_SIZE, total, showTotal: true }"
      @page-change="(page: number) => emit('update:query', { rpage: page })"
    >
      <template #empty>
        <div class="sub">{{ loaded ? "没有运行记录" : error ? "读取失败" : "读取中…" }}</div>
      </template>
      <template #columns>
        <a-table-column title="类型" :width="90"
          ><template #cell="{ record }">{{ kindLabel(record.kind) }}</template></a-table-column
        >
        <a-table-column title="状态" :width="80">
          <template #cell="{ record }">
            <span :class="`tone-${runTone(record.status)}`">{{ runStatusLabel(record.status) }}</span>
            <div v-if="record.status === 'running'" class="sub">
              {{ toNum(record.subjects_done) }}/{{ toNum(record.subjects_total) }} 个标的
            </div>
          </template>
        </a-table-column>
        <a-table-column title="计划时刻" :width="190"
          ><template #cell="{ record }">{{ formatInstant(record.scheduled_at, zone) }}</template></a-table-column
        >
        <a-table-column title="开始" :width="190"
          ><template #cell="{ record }">{{ formatInstant(record.started_at, zone) }}</template></a-table-column
        >
        <a-table-column title="结束" :width="190"
          ><template #cell="{ record }">{{ formatInstant(record.finished_at, zone) }}</template></a-table-column
        >
        <a-table-column title="耗时" :width="100"
          ><template #cell="{ record }">{{ runDurationText(record, nowMs) }}</template></a-table-column
        >
        <a-table-column title="读取区间" :width="210">
          <template #cell="{ record }">
            <template v-if="record.kind === 'tick' || record.kind === 'range'">
              <div>{{ formatInstant(record.range_start, zone) }}</div>
              <div>～ {{ formatInstant(record.range_end, zone) }}</div>
            </template>
            <span v-else>—</span>
          </template>
        </a-table-column>
        <a-table-column title="行数（读/新增/变化）" :width="150">
          <template #cell="{ record }"
            >{{ toNum(record.rows_read) }} / {{ toNum(record.rows_added) }} / {{ toNum(record.rows_changed) }}</template
          >
        </a-table-column>
        <a-table-column :title="remoteEnabled ? '分区（导出/定稿/上传）' : '分区（导出/定稿）'" :width="150">
          <template #cell="{ record }"
            >{{ record.partitions_exported || 0 }} / {{ record.partitions_finalized || 0
            }}<template v-if="remoteEnabled"> / {{ record.partitions_uploaded || 0 }}</template></template
          >
        </a-table-column>
        <a-table-column title="请求 / 读取" :width="130">
          <template #cell="{ record }">{{ toNum(record.request_count) }} / {{ formatBytes(toNum(record.bytes_read)) }}</template>
        </a-table-column>
        <a-table-column title="无法补齐" :width="90">
          <template #cell="{ record }">
            <a-popover v-if="record.unrecoverable?.length" trigger="click">
              <a-link>{{ record.unrecoverable.length }} 段</a-link>
              <template #content>
                <div v-for="(item, index) in record.unrecoverable" :key="index" class="range-line">
                  {{ item.subject_id || "全部标的" }}{{ item.series_tag ? ` · ${item.series_tag}` : "" }}：{{
                    formatInstant(item.start_time, zone)
                  }}
                  ～ {{ formatInstant(item.end_time, zone) }}
                </div>
              </template>
            </a-popover>
            <span v-else>—</span>
          </template>
        </a-table-column>
        <a-table-column title="消息" :width="260">
          <template #cell="{ record }"
            ><div class="message">{{ record.message || "—" }}</div></template
          >
        </a-table-column>
        <a-table-column title="操作" :width="80" fixed="right">
          <template #cell="{ record }">
            <a-button v-if="runActive(record)" size="mini" :loading="canceling[record.run_id]" @click="confirmCancel(record)"
              >取消</a-button
            >
          </template>
        </a-table-column>
      </template>
    </a-table>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { cancelArchiveRun, listArchiveRuns, type ArchiveRun } from "@/api/archive";
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { formatBytes } from "@/api/modules/host-monitor";
import { usePolling } from "@/hooks/usePolling";
import { RequestGate } from "@/utils/request-gate";
import { formatInstant } from "@/utils/time-zone";
import {
  RUN_STATUSES,
  kindLabel,
  runActive,
  runDurationText,
  runStatusLabel,
  runTone,
  toNum,
  type ArchiveQuery
} from "./archive-model";

/**
 * 运行记录页签：状态筛选、是否列出成功的到点拉取、页码都在 URL 里。
 * 有排队或运行中的运行时每 10 秒刷新，其余 30 秒；轮询请求不弹全局错误。时刻都到秒。
 */
const PAGE_SIZE = 50;

const props = defineProps<{
  datasetId: string;
  query: ArchiveQuery;
  zone: string;
  nowMs: number;
  /** 任务的最近一次运行正在运行 */
  taskRunning: boolean;
  remoteEnabled?: boolean;
  /** 变化时重读（立即运行、补拉之后 URL 可能不变） */
  refreshKey?: number;
}>();
const emit = defineEmits<{ "update:query": [patch: Partial<ArchiveQuery>]; changed: [] }>();

const runs = ref<ArchiveRun[]>([]);
const total = ref(0);
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
const canceling = ref<Record<string, boolean>>({});
const gate = new RequestGate();

async function load() {
  const token = gate.next();
  loading.value = true;
  try {
    const rsp = await listArchiveRuns(
      {
        dataset_id: props.datasetId,
        status: props.query.status,
        include_ticks: props.query.ticks,
        page: { page: props.query.rpage, size: PAGE_SIZE }
      },
      { silentError: true }
    );
    if (!gate.isCurrent(token)) return;
    runs.value = rsp.runs || [];
    total.value = Number(rsp.page_result?.total || runs.value.length);
    loaded.value = true;
    error.value = "";
  } catch (cause) {
    if (cause instanceof StaleSpaceRequestError || !gate.isCurrent(token)) return;
    error.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

// 逐项比较：props.query 每次路由变化都是新对象，不能整体依赖它
watch(
  [() => props.datasetId, () => props.query.status, () => props.query.ticks, () => props.query.rpage, () => props.refreshKey],
  () => {
    loaded.value = false;
    void load();
  },
  { immediate: true }
);

// 有在跑的运行时加快刷新
const fast = computed(() => props.taskRunning || runs.value.some(runActive));
usePolling(load, 10_000, fast);
usePolling(load, 30_000, () => !fast.value);

function confirmCancel(run: ArchiveRun) {
  const id = run.run_id || "";
  Modal.confirm({
    title: `取消这次${kindLabel(run.kind)}？`,
    content: "已导出的分区仍会登记；到点拉取的区间由之后的复核补上。",
    okText: "取消运行",
    okButtonProps: { status: "warning" },
    cancelText: "不取消",
    onBeforeOk: async () => {
      canceling.value = { ...canceling.value, [id]: true };
      try {
        await cancelArchiveRun(id);
      } catch {
        return false;
      } finally {
        canceling.value = Object.fromEntries(Object.entries(canceling.value).filter(([key]) => key !== id));
      }
      Message.success("已取消");
      void load();
      emit("changed");
      return true;
    }
  });
}
</script>

<style scoped>
.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 12px;
  margin-bottom: var(--moox-space-2);
}

.sub {
  color: var(--color-text-3);
  font-size: 12px;
}

.message {
  white-space: pre-wrap;
  word-break: break-all;
}

.range-line {
  line-height: 22px;
}

.load-error {
  margin-bottom: var(--moox-space-2);
  color: rgb(var(--red-6));
}

.tone-ok {
  color: rgb(var(--green-6));
}

.tone-warn {
  color: rgb(var(--orange-6));
}

.tone-bad {
  color: rgb(var(--red-6));
}

.tone-muted {
  color: var(--color-text-3);
}
</style>
