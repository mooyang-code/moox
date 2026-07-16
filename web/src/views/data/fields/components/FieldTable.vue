<template>
  <a-table
    row-key="field_id"
    size="small"
    :bordered="{ cell: true }"
    :loading="loading"
    :data="rows"
    :pagination="pagination"
    :scroll="{ x: 980, y: 'calc(100vh - 320px)' }"
    :row-selection="{ type: 'checkbox', showCheckedAll: true }"
    :selected-keys="selectedKeys"
    @select="$emit('select', $event)"
    @select-all="selectAll"
    @row-click="onRowClick"
    @page-change="$emit('pageChange', $event)"
    @page-size-change="$emit('pageSizeChange', $event)"
    @sorter-change="onSorterChange"
  >
    <template #columns>
      <a-table-column title="字段" data-index="field_id" :width="210" sorter>
        <template #cell="{ record }">
          <div class="field-name">
            <strong>{{ record.name }}</strong>
            <span>{{ record.field_id }}</span>
          </div>
        </template>
      </a-table-column>
      <a-table-column title="值类型" data-index="value_type" :width="120">
        <template #cell="{ record }">{{ optionLabel(fieldValueTypeOptions, record.value_type) }}</template>
      </a-table-column>
      <a-table-column title="字段组" data-index="group_id" :width="190">
        <template #cell="{ record }">
          <a-tooltip :content="groupPath(groups, record.group_id)"><span class="ellipsis">{{ groupPath(groups, record.group_id) || '-' }}</span></a-tooltip>
        </template>
      </a-table-column>
      <a-table-column title="单位" data-index="unit" :width="100"><template #cell="{ record }">{{ record.unit || '-' }}</template></a-table-column>
      <a-table-column title="状态" data-index="status" :width="100">
        <template #cell="{ record }"><a-tag size="small" :color="statusColor(record.status)">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag></template>
      </a-table-column>
      <a-table-column title="更新时间" data-index="updated_at" :width="180" sorter>
        <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
      </a-table-column>
      <a-table-column title="操作" :width="70" align="center" fixed="right">
        <template #cell="{ record }">
          <a-tooltip content="编辑字段"><a-button size="mini" type="text" @click.stop="$emit('edit', record)"><icon-edit /></a-button></a-tooltip>
        </template>
      </a-table-column>
    </template>
    <template #empty>
      <a-empty :description="emptyText" />
    </template>
  </a-table>
</template>

<script setup lang="ts">
import type { Field, FieldGroup } from '@/api/storage/types';
import { fieldValueTypeOptions, formatTime, optionLabel, statusColor } from '@/views/data/shared/metadata-utils';
import { groupPath } from '../field-workbench';

const props = defineProps<{
  rows: Field[];
  groups: FieldGroup[];
  loading: boolean;
  pagination: Record<string, unknown>;
  selectedKeys: string[];
  emptyText: string;
}>();

const emit = defineEmits<{
  select: [keys: string[]];
  edit: [field: Field];
  pageChange: [page: number];
  pageSizeChange: [pageSize: number];
  sort: [field: 'sort_order' | 'field_id' | 'updated_at', order: 'asc' | 'desc'];
}>();

function selectAll(checked: boolean) {
  emit('select', checked ? props.rows.map((item) => item.field_id) : []);
}

function onRowClick(record: Field, event?: MouseEvent) {
  const target = event?.target as HTMLElement | null;
  if (target?.closest('button, a, input, .arco-checkbox, .arco-table-checkbox')) return;
  emit('edit', record);
}

function onSorterChange(field: string, direction: string) {
  const sort = field === 'updated_at' ? 'updated_at' : field === 'field_id' ? 'field_id' : 'sort_order';
  emit('sort', sort, direction === 'descend' ? 'desc' : 'asc');
}
</script>

<style scoped>
.field-name { display: flex; min-width: 0; align-items: baseline; gap: var(--moox-space-1); line-height: 1.35; white-space: nowrap; }
.field-name strong, .field-name span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.field-name strong { min-width: 0; color: var(--color-text-1); }
.field-name span { min-width: 0; color: var(--color-text-3); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
.ellipsis { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
:deep(.arco-table-tr) { cursor: pointer; }
</style>
