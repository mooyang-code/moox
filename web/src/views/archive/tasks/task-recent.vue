<template>
  <div class="task-recent">
    <div class="filters">
      <span>标的</span>
      <a-select
        :model-value="subject || undefined"
        placeholder="搜索或输入标的 ID"
        allow-search
        allow-create
        allow-clear
        :filter-option="false"
        :loading="searching"
        style="width: 320px; max-width: 100%"
        @search="search"
        @change="(value: unknown) => emit('update:subject', String(value || ''))"
      >
        <a-option v-for="item in options" :key="item.subject_id" :value="item.subject_id"
          >{{ item.subject_id
          }}<template v-if="item.name && item.name !== item.subject_id"> · {{ item.name }}</template></a-option
        >
      </a-select>
      <template v-if="subject">
        <a-button size="small" :disabled="loading" @click="reload">刷新</a-button>
      </template>
    </div>

    <div v-if="!subject" class="sub">选一个标的，查看执行器暂存库里它最近的行（每页 {{ PAGE_SIZE }} 行）。</div>
    <template v-else>
      <div v-if="error" class="load-error">读取最新数据失败：{{ error }} · <a-link @click="load(cursor)">重试</a-link></div>
      <div v-if="result" class="sub head-note">
        <template v-if="result.staged_until"
          >暂存库已从 Storage 读到 {{ formatInstant(result.staged_until, zone) }}，之后的行可能还没读到</template
        >
        <template v-else>暂存库还没有从 Storage 读过</template>；这里包括还没导出成文件的行，暂存行保留到分区定稿，至少有最近 3
        天。
      </div>
      <a-table
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="tableRows"
        :pagination="false"
        :scroll="{ x: Math.max(600, columns.length * 140) }"
      >
        <template #empty>
          <div class="sub">{{ loading ? "读取中…" : error ? "读取失败" : "暂存库里没有这个标的的行" }}</div>
        </template>
        <template #columns>
          <a-table-column v-for="(column, index) in columns" :key="column.name" :title="column.name" :width="columnWidth(column)">
            <template #cell="{ record }">
              <span :class="{ missing: record.cells[index] === null || record.cells[index] === '' }">{{
                recentCellText(record.cells[index], column.type, zone)
              }}</span>
            </template>
          </a-table-column>
        </template>
      </a-table>
      <div v-if="result" class="pager">
        <span class="sub">{{ cursors.length ? `第 ${cursors.length + 1} 页` : "最新" }} · {{ tableRows.length }} 行</span>
        <a-button size="small" :disabled="loading || !cursors.length" @click="newest">回到最新</a-button>
        <a-button size="small" :disabled="loading || !hasEarlier" @click="earlier">更早</a-button>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { fetchArchiveRecentRows, type ArchiveRecentColumn, type ArchiveRecentRowsRsp } from "@/api/archive";
import { listSubjects } from "@/api/storage/metadata";
import type { Subject } from "@/api/storage/types";
import { RequestGate } from "@/utils/request-gate";
import { formatInstant } from "@/utils/time-zone";
import { recentCellText } from "../shared/archive-model";

/**
 * 最新数据页签：某个标的在执行器暂存库里最近的行，按 data_time 倒序，每页 200 行，“更早”用当前页最后一行的时间翻页。
 * 标的在 URL 里（?subject=）；每次读取都要重新取一次性 ticket（接口封装里做）。不轮询，换标的、翻页、点刷新时读取。
 */
const PAGE_SIZE = 200;

const props = defineProps<{ spaceId: string; datasetId: string; subject: string; zone: string }>();
const emit = defineEmits<{ "update:subject": [subject: string] }>();

// ---- 标的搜索：Storage 的 ListSubjects，keyword 匹配对象 ID 与名称 ----
const options = ref<Subject[]>([]);
const searching = ref(false);
const searchGate = new RequestGate();
let searchTimer: ReturnType<typeof setTimeout> | undefined;

function search(keyword: string) {
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(() => void loadOptions(keyword.trim()), 300);
}

async function loadOptions(keyword: string) {
  const token = searchGate.next();
  searching.value = true;
  try {
    const rsp = await listSubjects({ space_id: props.spaceId, keyword: keyword || undefined, page: { page: 1, size: 20 } });
    if (searchGate.isCurrent(token)) options.value = rsp.subjects || [];
  } catch {
    // 搜索失败不影响手动输入标的 ID
    if (searchGate.isCurrent(token)) options.value = [];
  } finally {
    if (searchGate.isCurrent(token)) searching.value = false;
  }
}

// ---- 最新数据 ----
const result = ref<ArchiveRecentRowsRsp | null>(null);
const loading = ref(false);
const error = ref("");
/** 已经翻过的页的 before（第一页没有）；当前页的 before 是最后一个 */
const cursors = ref<string[]>([]);
const cursor = computed(() => cursors.value.at(-1) || "");
const gate = new RequestGate();
let controller: AbortController | null = null;

const columns = computed<ArchiveRecentColumn[]>(() => result.value?.columns || []);
const tableRows = computed(() => (result.value?.rows || []).map((cells, index) => ({ key: index, cells })));
// 不满一页说明没有更早的行了
const hasEarlier = computed(() => tableRows.value.length >= PAGE_SIZE);

async function load(before: string) {
  if (!props.subject) return;
  const token = gate.next();
  controller?.abort();
  controller = new AbortController();
  loading.value = true;
  error.value = "";
  try {
    const rsp = await fetchArchiveRecentRows(
      { space: props.spaceId, dataset: props.datasetId, subject: props.subject, before: before || undefined, limit: PAGE_SIZE },
      controller.signal
    );
    if (!gate.isCurrent(token)) return;
    result.value = rsp;
  } catch (cause) {
    if (!gate.isCurrent(token)) return;
    error.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

function reload() {
  void load(cursor.value);
}
function newest() {
  cursors.value = [];
  void load("");
}
function earlier() {
  const rows = result.value?.rows || [];
  const last = rows.at(-1)?.[0];
  if (!last) return;
  cursors.value = [...cursors.value, last];
  void load(last);
}

watch(
  [() => props.datasetId, () => props.subject],
  () => {
    result.value = null;
    cursors.value = [];
    error.value = "";
    void load("");
  },
  { immediate: true }
);
onBeforeUnmount(() => {
  controller?.abort();
  if (searchTimer) clearTimeout(searchTimer);
});

const columnWidth = (column: ArchiveRecentColumn) => (column.type === "time" ? 200 : column.name === "subject_id" ? 160 : 120);

// 标的下拉属于当前空间：切空间时重读（也包括首次）
watch(
  () => props.spaceId,
  () => {
    options.value = [];
    void loadOptions("");
  },
  { immediate: true }
);
</script>

<style scoped>
.filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: var(--moox-space-2);
}

.head-note {
  margin-bottom: var(--moox-space-2);
}

.pager {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  margin-top: var(--moox-space-2);
}

.sub {
  color: var(--color-text-3);
  font-size: 12px;
}

.missing {
  color: var(--color-text-4);
}

.load-error {
  margin-bottom: var(--moox-space-2);
  color: rgb(var(--red-6));
}
</style>
