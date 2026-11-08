<template>
  <div class="activity-log">
    <a-space v-if="showFilters" wrap class="activity-log-filters">
      <a-input v-model="filters.category" placeholder="类别，如 collector.subject_change" allow-clear style="width: 260px" />
      <a-input v-model="filters.source" placeholder="来源模块" allow-clear style="width: 150px" />
      <ZonedRangePicker v-model="filters.range" :zone="zone" style="width: 400px" />
      <a-button type="primary" @click="search">
        <template #icon><icon-search /></template>
        查询
      </a-button>
    </a-space>
    <a-alert v-if="loadError" type="error" class="activity-log-error">
      操作记录读取失败：{{ loadError }}
      <template #action><a-button size="mini" @click="load">重试</a-button></template>
    </a-alert>
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
          <template #cell="{ record }">{{ formatTime(record.created_at, zone) }}</template>
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
import { activityDetailText, activityLevelColor, activityTimeText } from "./activity-log";
import ZonedRangePicker from "@/components/zoned-date-picker/zoned-range-picker.vue";

const props = withDefaults(
  defineProps<{
    spaceId?: string;
    // 只看不属于任何空间的全局事件（如登录记录），与 spaceId 二选一。
    global?: boolean;
    /** 时间的显示时区：当前空间的记录用空间时区，系统事件用 UTC。组件不自己读空间，由调用方传入。 */
    zone?: string;
    category?: string;
    pageSize?: number;
    showFilters?: boolean;
    showSpace?: boolean;
  }>(),
  { spaceId: "", global: false, zone: "UTC", category: "", pageSize: 20, showFilters: false, showSpace: false }
);

const guard = createLatestRequestGuard();
const loading = ref(false);
// 读取失败时显示原因和重试，不能只留一张空表让人以为没有记录。
const loadError = ref("");
const items = ref<ActivityLogItem[]>([]);
const page = ref(1);
const total = ref(0);
// range 是 [起, 止) 毫秒时刻，按调用方给的时区选择
const filters = ref({ category: props.category, source: "", range: null as [number, number] | null });
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
      since: range ? new Date(range[0]).toISOString() : undefined,
      until: range ? new Date(range[1]).toISOString() : undefined,
      page: { page: page.value, size: props.pageSize }
    });
    if (!request.isLatest()) return;
    items.value = data.items || [];
    total.value = Number(data.page?.total) || items.value.length;
    loadError.value = "";
  } catch (error) {
    if (!request.isLatest()) return;
    loadError.value = error instanceof Error && error.message ? error.message : "请稍后重试";
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
.activity-log-error {
  margin-bottom: var(--moox-space-2);
}
</style>
