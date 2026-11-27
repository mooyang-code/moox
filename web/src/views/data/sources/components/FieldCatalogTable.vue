<template>
  <a-table
    row-key="field_id"
    size="small"
    class="field-catalog-table"
    :bordered="{ cell: true }"
    :loading="loading"
    :data="rows"
    :pagination="pagination"
    :scroll="{ x: 1120, y: 'calc(100vh - 360px)' }"
    @row-click="onRowClick"
    @page-change="emit('pageChange', $event)"
    @page-size-change="emit('pageSizeChange', $event)"
  >
    <template #columns>
      <a-table-column title="字段" data-index="field_id" :width="260" :sortable="{ sortDirections: ['ascend', 'descend'] }">
        <template #cell="{ record }">
          <div class="field-name-cell">
            <div class="field-name">
              <strong :title="record.display_name">{{ record.display_name }}</strong>
              <span v-if="record.display_name !== record.field_id" :title="record.field_id">{{ record.field_id }}</span>
            </div>
            <a-tooltip content="复制字段 ID">
              <a-button
                class="copy-btn"
                size="mini"
                type="text"
                :aria-label="`复制字段 ${record.field_id}`"
                @click.stop="copyText(record.field_id)"
              >
                <template #icon><icon-copy /></template>
              </a-button>
            </a-tooltip>
          </div>
        </template>
      </a-table-column>

      <a-table-column title="类别" :width="88">
        <template #cell="{ record }">
          <a-tag size="small" :color="fieldCategoryColor(record.category)">{{ fieldCategoryLabel(record.category) }}</a-tag>
        </template>
      </a-table-column>

      <a-table-column title="值类型" :width="96">
        <template #cell="{ record }">{{ fieldValueTypeLabel(record.value_type) }}</template>
      </a-table-column>

      <a-table-column title="分组" :width="110" :ellipsis="true" :tooltip="true">
        <template #cell="{ record }">{{ record.group }}</template>
      </a-table-column>

      <a-table-column title="来源与适用范围" :width="270" :ellipsis="true" :tooltip="true">
        <template #cell="{ record }">{{ fieldSourceSummary(record) }}</template>
      </a-table-column>

      <a-table-column title="缺失填充" :width="96">
        <template #cell="{ record }">{{ fillStrategyLabel(record.fill) }}</template>
      </a-table-column>

      <a-table-column title="已落地数据集" :width="190">
        <template #cell="{ record }">
          <a-tooltip v-if="record.datasets.length" :content="datasetTooltip(record)">
            <span class="landed-text">{{ datasetSummary(record) }}</span>
          </a-tooltip>
          <span v-else class="muted">未落地</span>
        </template>
      </a-table-column>
    </template>

    <template #empty>
      <a-empty :description="emptyText" />
    </template>
  </a-table>
</template>

<script setup lang="ts">
import type { TableData } from "@arco-design/web-vue";
import { copyText } from "@/utils/clipboard";
import { fieldValueTypeLabel, fillStrategyLabel } from "@/views/data/shared/metadata-utils";
import { fieldCategoryColor, fieldCategoryLabel, fieldSourceSummary, type CatalogFieldItem } from "../field-catalog";

defineOptions({ name: "FieldCatalogTable" });

defineProps<{
  rows: CatalogFieldItem[];
  loading: boolean;
  pagination: Record<string, unknown>;
  emptyText: string;
}>();

const emit = defineEmits<{
  detail: [field: CatalogFieldItem];
  pageChange: [page: number];
  pageSizeChange: [pageSize: number];
}>();

function onRowClick(record: TableData, event?: Event) {
  const target = event?.target as HTMLElement | null;
  if (target?.closest("button, a, input")) return;
  emit("detail", record as CatalogFieldItem);
}

function datasetSummary(item: CatalogFieldItem): string {
  if (!item.datasets.length) return "未落地";
  if (item.datasets.length === 1) return item.datasets[0].dataset_name;
  return `${item.datasets.length} 个数据集`;
}

function datasetTooltip(item: CatalogFieldItem): string {
  return item.datasets
    .map(d => {
      const freq = d.freq ? ` (${d.freq})` : d.data_kind === "record" ? " (记录型)" : "";
      const disabled = d.column_status !== "active" ? " [已停用]" : "";
      return `${d.dataset_name}${freq}${disabled}`;
    })
    .join("、");
}
</script>

<style scoped>
.field-name-cell {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-1);
}

.field-name {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  align-items: baseline;
  gap: var(--moox-space-1);
  line-height: 1.35;
  white-space: nowrap;
}

.field-name strong,
.field-name span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.field-name strong {
  min-width: 0;
  color: var(--color-text-1);
}

.field-name span {
  min-width: 0;
  color: var(--color-text-3);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}

.copy-btn {
  flex: 0 0 auto;
  opacity: 0;
  transition: opacity 0.14s ease;
}

:deep(.arco-table-tr) {
  cursor: pointer;
}

:deep(.arco-table-tr:hover) .copy-btn,
.copy-btn:focus-visible {
  opacity: 1;
}

.landed-text {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.muted {
  color: var(--color-text-3);
}
</style>
