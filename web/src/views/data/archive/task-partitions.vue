<template>
  <div class="task-partitions">
    <div class="filters">
      <span>月份</span>
      <a-month-picker
        :model-value="query.from || undefined"
        value-format="YYYY-MM"
        placeholder="起"
        style="width: 130px"
        @change="(value: unknown) => emit('update:query', { from: String(value || ''), ppage: 1 })"
      />
      <span>～</span>
      <a-month-picker
        :model-value="query.to || undefined"
        value-format="YYYY-MM"
        placeholder="止"
        style="width: 130px"
        @change="(value: unknown) => emit('update:query', { to: String(value || ''), ppage: 1 })"
      />
    </div>
    <div v-if="error" class="load-error">
      读取分区失败：{{ error }}<template v-if="partitions.length">（下面是上次读到的）</template> ·
      <a-link @click="load">重试</a-link>
    </div>
    <a-table
      row-key="partition_key"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading && !loaded"
      :data="partitions"
      :scroll="{ x: 1400 }"
      :pagination="{ current: query.ppage, pageSize: PAGE_SIZE, total, showTotal: true }"
      @page-change="(page: number) => emit('update:query', { ppage: page })"
    >
      <template #empty>
        <div class="sub">{{ loaded ? "没有分区" : error ? "读取失败" : "读取中…" }}</div>
      </template>
      <template #columns>
        <!-- 分区键是本地日或月的标签，不是时刻，原样显示 -->
        <a-table-column title="分区" data-index="partition_key" :width="110" />
        <a-table-column title="状态" :width="80"
          ><template #cell="{ record }">{{ partitionStatusLabel(record.status) }}</template></a-table-column
        >
        <a-table-column title="数据时间" :width="210">
          <template #cell="{ record }">
            <div>{{ formatInstant(record.min_data_time, zone) }}</div>
            <div>～ {{ formatInstant(record.max_data_time, zone) }}</div>
          </template>
        </a-table-column>
        <a-table-column title="行数" :width="90"
          ><template #cell="{ record }">{{ toNum(record.row_count) }}</template></a-table-column
        >
        <a-table-column title="标的数" :width="80"
          ><template #cell="{ record }">{{ toNum(record.subject_count) }}</template></a-table-column
        >
        <a-table-column title="文件" :width="300">
          <template #cell="{ record }">
            <a-link class="mono path" title="点击复制完整路径" @click="copy(record.file_path)">{{
              record.file_path || "—"
            }}</a-link>
            <div class="sub">
              {{ formatBytes(toNum(record.file_bytes)) }}
              <template v-if="record.file_sha256">
                ·
                <a-link class="mono" title="点击复制完整 sha256" @click="copy(record.file_sha256)">{{
                  record.file_sha256.slice(0, 12)
                }}</a-link>
              </template>
            </div>
          </template>
        </a-table-column>
        <a-table-column title="导出次数" data-index="export_count" :width="80" />
        <a-table-column title="导出 / 定稿时间" :width="210">
          <template #cell="{ record }">
            <div>{{ formatInstant(record.exported_at, zone) }}</div>
            <div>{{ formatInstant(record.finalized_at, zone) }}</div>
          </template>
        </a-table-column>
        <a-table-column v-if="remoteEnabled" title="远端副本" :width="140">
          <template #cell="{ record }">
            <span :title="remoteTitle(record)">{{ remoteStateLabel(record.remote_state, record.remote_key) }}</span>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="70" fixed="right">
          <template #cell="{ record }">
            <a-button size="mini" @click="emit('update:query', { partition: record.partition_key })">预览</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <PartitionPreview
      v-if="query.partition"
      :dataset-id="datasetId"
      :partition-key="query.partition"
      :zone="zone"
      @close="emit('update:query', { partition: '' })"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { listArchivePartitions, type ArchivePartition } from "@/api/archive";
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { formatBytes } from "@/api/modules/host-monitor";
import { RequestGate } from "@/utils/request-gate";
import { formatInstant } from "@/utils/time-zone";
import { partitionStatusLabel, remoteStateLabel, toNum, type ArchiveQuery } from "./archive-model";
import PartitionPreview from "./partition-preview.vue";

/** 分区页签：后端按分区键倒序；起止月份、页码、预览的分区都在 URL 里。不轮询，切到页签、翻页、改筛选时读取。 */
const PAGE_SIZE = 50;

const props = defineProps<{ datasetId: string; query: ArchiveQuery; zone: string; remoteEnabled?: boolean }>();
const emit = defineEmits<{ "update:query": [patch: Partial<ArchiveQuery>] }>();

const partitions = ref<ArchivePartition[]>([]);
const total = ref(0);
const loading = ref(false);
const loaded = ref(false);
const error = ref("");
const gate = new RequestGate();

async function load() {
  const token = gate.next();
  loading.value = true;
  try {
    const rsp = await listArchivePartitions({
      dataset_id: props.datasetId,
      from_month: props.query.from || undefined,
      to_month: props.query.to || undefined,
      page: { page: props.query.ppage, size: PAGE_SIZE }
    });
    if (!gate.isCurrent(token)) return;
    partitions.value = rsp.partitions || [];
    total.value = Number(rsp.page_result?.total || partitions.value.length);
    loaded.value = true;
    error.value = "";
  } catch (cause) {
    if (cause instanceof StaleSpaceRequestError || !gate.isCurrent(token)) return;
    error.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

watch(
  () => [props.datasetId, props.query.from, props.query.to, props.query.ppage],
  () => {
    loaded.value = false;
    void load();
  },
  { immediate: true }
);

async function copy(text?: string) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    Message.success("已复制");
  } catch {
    Message.warning("复制失败，请手动选择");
  }
}

// 批 2 之后：远端副本的细节放在悬停提示里
function remoteTitle(partition: ArchivePartition): string | undefined {
  if (partition.remote_state === "verified")
    return `校验于 ${formatInstant(partition.remote_verified_at, props.zone)}\n${partition.remote_key || ""}`;
  if (partition.remote_state === "failed") return partition.remote_error || undefined;
  if (partition.remote_state === "pending" && partition.remote_key) return `远端只有旧版本：${partition.remote_key}`;
  return undefined;
}
</script>

<style scoped>
.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: var(--moox-space-2);
}

.sub {
  color: var(--color-text-3);
  font-size: 12px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.path {
  word-break: break-all;
}

.load-error {
  margin-bottom: var(--moox-space-2);
  color: rgb(var(--red-6));
}
</style>
