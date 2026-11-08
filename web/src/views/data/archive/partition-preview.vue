<template>
  <section class="partition-preview" :aria-label="`分区 ${partitionKey} 预览`">
    <header>
      <h4>分区 {{ partitionKey }} 预览</h4>
      <a-button size="mini" @click="emit('close')">关闭</a-button>
    </header>
    <div v-if="error" class="load-error">读取分区失败：{{ error }} · <a-link @click="load">重试</a-link></div>
    <div v-else-if="!partition" class="sub">读取中…</div>
    <template v-else>
      <div class="meta">
        <span>{{ formatInstant(partition.range_start, zone) }} ～ {{ formatInstant(partition.range_end, zone) }}</span>
        <span>最近运行 {{ partition.last_run_id || "—" }}</span>
        <span>执行器 {{ partition.worker_id || "—" }}</span>
      </div>
      <div class="sub">列：{{ (partition.columns || []).join("、") || "—" }}</div>
      <a-table
        v-if="table"
        :data="tableRows"
        :pagination="false"
        size="mini"
        :bordered="{ cell: true }"
        :scroll="{ x: 'max-content' }"
      >
        <template #columns>
          <a-table-column v-for="(column, index) in table.header" :key="index" :title="column">
            <template #cell="{ record }">{{ record[index] }}</template>
          </a-table-column>
        </template>
      </a-table>
      <pre v-else class="raw">{{ partition.preview || "没有预览" }}</pre>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { getArchivePartition, type ArchivePartition } from "@/api/archive";
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { RequestGate } from "@/utils/request-gate";
import { formatInstant } from "@/utils/time-zone";
import { parsePreviewCsv } from "./archive-model";

/** 分区预览（?partition=）：列名和前 20 行；CSV 解析失败就显示原文。失败只在这里显示。 */
const props = defineProps<{ datasetId: string; partitionKey: string; zone: string }>();
const emit = defineEmits<{ close: [] }>();

const partition = ref<ArchivePartition | null>(null);
const error = ref("");
const gate = new RequestGate();

async function load() {
  const token = gate.next();
  error.value = "";
  partition.value = null;
  try {
    const rsp = await getArchivePartition(props.datasetId, props.partitionKey, { silentError: true });
    if (gate.isCurrent(token)) partition.value = rsp.partition || {};
  } catch (cause) {
    if (cause instanceof StaleSpaceRequestError || !gate.isCurrent(token)) return;
    error.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
  }
}

watch(() => [props.datasetId, props.partitionKey], load, { immediate: true });

const table = computed(() => parsePreviewCsv(partition.value?.preview || ""));
// a-table 要对象行：用列下标做键
const tableRows = computed(() => (table.value?.rows || []).map(row => ({ ...row })));
</script>

<style scoped>
.partition-preview {
  margin-top: var(--moox-space-3);
  padding: var(--moox-space-3);
  border: 1px solid var(--color-border-2);
}

header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
}

h4 {
  margin: 0;
  font-size: 14px;
}

.meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
  margin-bottom: 4px;
}

.sub {
  margin-bottom: 6px;
  color: var(--color-text-3);
  font-size: 12px;
}

.raw {
  max-height: 320px;
  overflow: auto;
  white-space: pre;
  font-size: 12px;
}

.load-error {
  color: rgb(var(--red-6));
}
</style>
