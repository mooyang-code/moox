<template>
  <div class="activity-log">
    <a-space v-if="showFilters" wrap class="activity-log-filters">
      <a-input v-model="filters.category" placeholder="类别，如 collector.subject_change" allow-clear style="width: 260px" />
      <a-input v-model="filters.source" placeholder="来源模块" allow-clear style="width: 150px" />
      <a-range-picker v-model="filters.range" show-time style="width: 360px" />
      <a-button type="primary" @click="search">
        <template #icon><icon-search /></template>
        查询
      </a-button>
    </a-space>
    <a-table
      row-key="id"
      size="small"
      :data="items"
      :loading="loading"
      :bordered="{ cell: true }"
      :pagination="paginationConfig"
      @page-change="onPageChange"
    >
      <template #columns>
        <a-table-column title="时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.created_at) }}</template>
        </a-table-column>
        <a-table-column v-if="showSpace" title="空间" data-index="space_id" :width="110">
          <template #cell="{ record }">{{ record.space_id || "全局" }}</template>
        </a-table-column>
        <a-table-column title="类别" :width="220">
          <template #cell="{ record }"
            ><a-tag size="small" color="arcoblue">{{ record.category }}</a-tag></template
          >
        </a-table-column>
        <a-table-column title="级别" :width="80" align="center">
          <template #cell="{ record }"
            ><a-tag size="small" :color="levelColor(record.level)">{{ record.level }}</a-tag></template
          >
        </a-table-column>
        <a-table-column title="内容" data-index="title" />
        <a-table-column title="详情" :width="80" align="center">
          <template #cell="{ record }">
            <a-button type="text" size="mini" @click="showDetail(record)">查看</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>
    <a-modal v-model:visible="detailVisible" :footer="false" width="720px" title="活动详情">
      <pre class="activity-detail">{{ detailText }}</pre>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { listActivityLogs, type ActivityLogItem } from "@/api/admin/activity";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { activityDetailText, activityLevelColor, activityTimeText, rangeToISO } from "./activity-log";

const props = withDefaults(
  defineProps<{
    spaceId?: string;
    // 只看不属于任何空间的全局事件（如登录记录），与 spaceId 二选一。
    global?: boolean;
    category?: string;
    pageSize?: number;
    showFilters?: boolean;
    showSpace?: boolean;
  }>(),
  { spaceId: "", global: false, category: "", pageSize: 20, showFilters: false, showSpace: false }
);

const guard = createLatestRequestGuard();
const loading = ref(false);
const items = ref<ActivityLogItem[]>([]);
const page = ref(1);
const total = ref(0);
const filters = ref({ category: props.category, source: "", range: [] as string[] });
const detailVisible = ref(false);
const detailText = ref("");

const paginationConfig = computed(() => ({ current: page.value, pageSize: props.pageSize, total: total.value, showTotal: true }));
const levelColor = activityLevelColor;
const formatTime = activityTimeText;

async function load() {
  const request = guard.begin();
  loading.value = true;
  try {
    const range = filters.value.range;
    const data = await listActivityLogs({
      space_id: props.global ? undefined : props.spaceId || undefined,
      global: props.global || undefined,
      category: filters.value.category.trim() || undefined,
      source: filters.value.source.trim() || undefined,
      since: range?.[0] ? rangeToISO(range[0]) : undefined,
      until: range?.[1] ? rangeToISO(range[1]) : undefined,
      page: { page: page.value, size: props.pageSize }
    });
    if (!request.isLatest()) return;
    items.value = data.items || [];
    total.value = Number(data.page?.total) || items.value.length;
  } catch (error) {
    if (!request.isLatest()) return;
    console.error("获取活动日志失败:", error);
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function search() {
  page.value = 1;
  void load();
}

function onPageChange(value: number) {
  page.value = value;
  void load();
}

function showDetail(record: ActivityLogItem) {
  detailText.value = activityDetailText(record.detail);
  detailVisible.value = true;
}

watch(
  () => [props.spaceId, props.global, props.category],
  () => {
    filters.value.category = props.category;
    guard.invalidate();
    items.value = [];
    total.value = 0;
    detailVisible.value = false;
    search();
  }
);
onMounted(() => void load());
defineExpose({ reload: load });
</script>

<style scoped>
.activity-log-filters {
  margin-bottom: var(--moox-space-2);
}
.activity-detail {
  margin: 0;
  max-height: 420px;
  overflow: auto;
  padding: var(--moox-space-3);
  border-radius: 4px;
  background: var(--color-fill-2);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
