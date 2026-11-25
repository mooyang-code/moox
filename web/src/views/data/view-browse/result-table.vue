<template>
  <div class="view-result-table">
    <section v-if="mode === 'time_series' || mode === 'record'" class="result-pane">
      <a-table
        row-key="id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="rows"
        :pagination="false"
        :scroll="{ x: 'max-content', y: mode === 'time_series' ? 500 : 460 }"
      >
        <template #columns>
          <a-table-column title="序号" :width="72" align="center" fixed="left">
            <template #cell="{ rowIndex }">{{ pageOffset + rowIndex + 1 }}</template>
          </a-table-column>
          <a-table-column data-index="key" :width="mode === 'time_series' ? 180 : 200" fixed="left">
            <template #title>
              <span class="sortable-title">
                {{ mode === "time_series" ? "数据ID" : "记录ID" }}
                <span class="sort-arrows">
                  <button :class="sortArrowClass(keyField, 'asc')" @click.stop="emit('sort', keyField, 'asc')">▲</button>
                  <button :class="sortArrowClass(keyField, 'desc')" @click.stop="emit('sort', keyField, 'desc')">▼</button>
                </span>
              </span>
            </template>
          </a-table-column>
          <template v-if="mode === 'time_series'">
            <a-table-column
              v-for="column in shownSystemColumns"
              :key="column.field"
              :data-index="column.dataIndex"
              :width="column.width"
            >
              <template #title>
                <span class="sortable-title">
                  {{ column.title }}
                  <span class="sort-arrows">
                    <button :class="sortArrowClass(column.field, 'asc')" @click.stop="emit('sort', column.field, 'asc')">
                      ▲
                    </button>
                    <button :class="sortArrowClass(column.field, 'desc')" @click.stop="emit('sort', column.field, 'desc')">
                      ▼
                    </button>
                  </span>
                </span>
              </template>
              <template v-if="column.field === 'series_tag'" #cell="{ record }">
                <span :title="record.seriesTag || undefined">{{ seriesTagDisplay(record.seriesTag) }}</span>
              </template>
            </a-table-column>
          </template>
          <a-table-column data-index="version" :width="230">
            <template v-if="mode === 'time_series'" #cell="{ record }">{{ formatInstant(record.version, zone) }}</template>
            <template #title>
              <span class="sortable-title">
                {{ mode === "time_series" ? "时间" : "版本" }}
                <span class="sort-arrows">
                  <button :class="sortArrowClass(versionField, 'asc')" @click.stop="emit('sort', versionField, 'asc')">▲</button>
                  <button :class="sortArrowClass(versionField, 'desc')" @click.stop="emit('sort', versionField, 'desc')">
                    ▼
                  </button>
                </span>
              </span>
            </template>
          </a-table-column>
          <a-table-column
            v-for="column in columnNames"
            :key="column"
            :width="adaptiveColumnWidth(column, columnTitle(column), rows)"
            :ellipsis="true"
            :tooltip="true"
          >
            <template #title>
              <span class="sortable-title">
                {{ columnTitle(column) }}
                <span class="sort-arrows">
                  <button :class="sortArrowClass(column, 'asc')" @click.stop="emit('sort', column, 'asc')">▲</button>
                  <button :class="sortArrowClass(column, 'desc')" @click.stop="emit('sort', column, 'desc')">▼</button>
                </span>
              </span>
            </template>
            <template #cell="{ record }">{{ record.values[column] || "-" }}</template>
          </a-table-column>
          <a-table-column v-if="mode === 'time_series' && $slots['row-extra']" :title="rowExtraTitle" :width="150" align="center">
            <template #cell="{ record }">
              <slot name="row-extra" :row-key="record.rawKey" :values="record.values" />
            </template>
          </a-table-column>
          <a-table-column title="操作" :width="mode === 'time_series' ? 120 : 92" align="center" fixed="right">
            <template #cell="{ record }">
              <a-button type="text" size="mini" @click="openDetail(record)">查看</a-button>
              <a-button
                v-if="mode === 'time_series' && klineAvailable"
                type="text"
                size="mini"
                :disabled="!record.rawKey?.subject_id"
                @click="emit('rowKline', record)"
                >K 线</a-button
              >
            </template>
          </a-table-column>
        </template>
      </a-table>
      <div v-if="hasQueried" class="preview-pager">
        <div class="preview-pager__actions">
          <a-tooltip content="上一页">
            <a-button size="small" shape="circle" :disabled="page <= 1 || loading" aria-label="上一页" @click="emit('prev')">
              <template #icon><icon-left /></template>
            </a-button>
          </a-tooltip>
          <a-tooltip content="下一页">
            <a-button size="small" shape="circle" :disabled="!hasMore || loading" aria-label="下一页" @click="emit('next')">
              <template #icon><icon-right /></template>
            </a-button>
          </a-tooltip>
        </div>
      </div>
    </section>

    <a-empty v-else description="无法识别该数据集的类型" />

    <a-modal v-model:visible="detailVisible" title="数据详情" width="820px" :footer="false">
      <div v-if="detailRow" class="detail-body">
        <a-descriptions :column="2" bordered>
          <a-descriptions-item :label="mode === 'time_series' ? '数据ID' : '记录ID'">{{ detailRow.key }}</a-descriptions-item>
          <a-descriptions-item :label="mode === 'time_series' ? '时间' : '版本'">{{
            mode === "time_series" ? formatInstant(detailRow.version, zone) : detailRow.version
          }}</a-descriptions-item>
          <a-descriptions-item v-if="mode === 'time_series' && detailRow.seriesTag" label="市场">{{
            seriesTagMarketText(detailRow.seriesTag)
          }}</a-descriptions-item>
          <a-descriptions-item v-if="mode === 'time_series' && detailRow.seriesTag" label="序列标签">{{
            detailRow.seriesTag
          }}</a-descriptions-item>
        </a-descriptions>
        <a-table :data="detailColumns" :pagination="false" :bordered="{ cell: true }" size="small" class="detail-table">
          <template #columns>
            <a-table-column title="字段名" data-index="name" :width="220" />
            <a-table-column title="字段值" data-index="value" :ellipsis="true" :tooltip="true" />
          </template>
        </a-table>
      </div>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import type { RecordRow } from "@/api/storage/types";
import { formatInstant } from "@/utils/time-zone";
import { seriesTagDisplay, seriesTagMarketText } from "@/views/data/shared/series-market";
import { adaptiveColumnWidth, rowsToColumnNames, type ViewBrowseMode, type ViewSortDirection } from "./view-browse-utils";
import type { ViewBrowseTableRow } from "./composables/use-view-kline";

defineOptions({ name: "ViewResultTable" });

const props = defineProps<{
  mode: ViewBrowseMode;
  rows: ViewBrowseTableRow[];
  /** 表格里的业务列（按顺序）。 */
  columnNames: string[];
  /** 查询返回的全部业务列；详情弹窗在没有限定显示列时列出它们。 */
  allColumnNames: string[];
  /** 调用方限定了显示列：详情弹窗只列表格里的列，其余列没有投影，列出来只会是一排“-”。 */
  restrictDetailColumns?: boolean;
  columnTitle: (name: string) => string;
  /** 时序表要显示的系统键（数据ID、时间始终显示）。 */
  systemColumns?: string[];
  sortField: string;
  sortDirection: ViewSortDirection;
  loading?: boolean;
  hasQueried?: boolean;
  hasMore?: boolean;
  page: number;
  pageSize: number;
  klineAvailable?: boolean;
  rowExtraTitle?: string;
  zone: string;
}>();

const emit = defineEmits<{
  (event: "sort", field: string, direction: ViewSortDirection): void;
  (event: "prev"): void;
  (event: "next"): void;
  (event: "rowKline", record: ViewBrowseTableRow): void;
}>();

const keyField = computed(() => (props.mode === "time_series" ? "subject_id" : "record_id"));
const versionField = computed(() => (props.mode === "time_series" ? "data_time" : "version"));
const pageOffset = computed(() => (props.page - 1) * props.pageSize);

const timeSeriesSystemColumns = [
  { field: "freq", dataIndex: "freq", title: "频率", width: 96 },
  { field: "series_tag", dataIndex: "seriesTag", title: "序列标签", width: 120 }
];
const shownSystemColumns = computed(() =>
  timeSeriesSystemColumns.filter(column => !props.systemColumns || props.systemColumns.includes(column.field))
);

function sortArrowClass(fieldName: string, direction: ViewSortDirection) {
  return {
    "sort-arrow": true,
    active: props.sortField === fieldName && props.sortDirection === direction
  };
}

const detailRow = ref<ViewBrowseTableRow>();
const detailVisible = ref(false);

function openDetail(row: ViewBrowseTableRow) {
  detailRow.value = row;
  detailVisible.value = true;
}

function rowToSyntheticRecord(row: ViewBrowseTableRow): RecordRow {
  return {
    key: { space_id: "", dataset_id: "", record_id: row.key, version: row.version },
    fields: Object.keys(row.values).map(name => ({
      field_id: name,
      value: { string_value: row.values[name] }
    }))
  };
}

const detailColumns = computed(() => {
  const row = detailRow.value;
  if (!row) return [];
  const names = props.restrictDetailColumns
    ? props.columnNames
    : rowsToColumnNames([rowToSyntheticRecord(row)], props.allColumnNames);
  return names.map(name => ({ name: props.columnTitle(name), value: row.values[name] || "-" }));
});

defineExpose({ openDetail });
</script>

<style scoped>
.view-result-table {
  width: 100%;
  max-width: 100%;
  min-width: 0;
}

.result-pane {
  width: 100%;
  min-width: 0;
  max-width: 100%;
  box-sizing: border-box;
  padding: var(--moox-space-3);
  overflow: hidden;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}

.result-pane :deep(.arco-table),
.result-pane :deep(.arco-table-container) {
  width: 100%;
}

.preview-pager {
  display: flex;
  flex-wrap: wrap;
  gap: 10px var(--moox-space-4);
  align-items: center;
  justify-content: flex-end;
  margin-top: var(--moox-space-3);
  color: var(--color-text-2);
  font-size: 13px;
}

.preview-pager__actions {
  display: inline-flex;
  align-items: center;
  gap: 10px;
}

.detail-body {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-4);
  max-height: min(680px, calc(100vh - 220px));
  padding-right: var(--moox-space-1);
  overflow-y: auto;
}

.detail-table {
  margin-top: var(--moox-space-1);
}

.sortable-title {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  max-width: 100%;
  white-space: nowrap;
}

.sort-arrows {
  display: inline-flex;
  flex-direction: column;
  gap: 1px;
  width: 12px;
}

.sort-arrow {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 12px;
  height: 9px;
  padding: 0;
  border: 0;
  color: var(--color-text-4);
  font-size: 9px;
  line-height: 1;
  background: transparent;
  cursor: pointer;
}

.sort-arrow:hover,
.sort-arrow.active {
  color: rgb(var(--primary-6));
}
</style>
