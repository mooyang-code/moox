<template>
  <a-table
    row-key="factor_id"
    size="small"
    class="definition-table"
    :bordered="{ cell: true }"
    :loading="loading"
    :data="rows"
    :pagination="pagination"
    :scroll="{ x: 1100, y: 'calc(100vh - 320px)' }"
    :row-selection="{ type: 'checkbox', showCheckedAll: true }"
    :selected-keys="selectedKeys"
    @select="$emit('select', $event.map(String))"
    @select-all="selectAll"
    @row-click="onRowClick"
    @page-change="$emit('pageChange', $event)"
    @page-size-change="$emit('pageSizeChange', $event)"
  >
    <template #columns>
      <a-table-column title="因子" data-index="factor_id" :width="220" :sortable="{ sortDirections: ['ascend', 'descend'] }">
        <template #cell="{ record }">
          <div class="factor-name">
            <strong>{{ record.factor.name || record.factor_id }}</strong>
            <span v-if="record.factor.name && record.factor.name !== record.factor_id">{{ record.factor_id }}</span>
          </div>
        </template>
      </a-table-column>
      <a-table-column title="类型" :width="100">
        <template #cell="{ record }">{{ factorTypeLabel(record.factor.factor_type) }}</template>
      </a-table-column>
      <a-table-column title="分类" :width="110">
        <template #cell="{ record }">{{ record.category }}</template>
      </a-table-column>
      <a-table-column title="可调参数" :width="140" :ellipsis="true" :tooltip="true">
        <template #cell="{ record }">{{ record.paramNames }}</template>
      </a-table-column>
      <a-table-column title="输出列" :width="170" :ellipsis="true" :tooltip="true">
        <template #cell="{ record }">{{ record.factor.outputs.join(", ") || "-" }}</template>
      </a-table-column>
      <a-table-column title="最大回看周期" :width="110" align="right">
        <template #cell="{ record }">{{ record.factor.lookback_periods }}</template>
      </a-table-column>
      <a-table-column title="使用情况" :width="240">
        <template #cell="{ record }">
          <span v-if="!record.chips.length" class="usage-empty">未被使用</span>
          <div v-else class="usage-chips">
            <button
              v-for="chip in record.chips"
              :key="chip.key"
              type="button"
              class="usage-chip"
              :title="`${chip.label}${chip.alias ? ` · ${chip.alias}` : ''}（${chip.statusLabel}）`"
              @click.stop="$emit('openTask', chip.setId)"
            >
              <span class="usage-dot" :class="`usage-dot--${chip.status}`"></span>
              {{ chip.label }}
            </button>
          </div>
        </template>
      </a-table-column>
      <a-table-column title="更新时间" data-index="updated_at" :width="170" :sortable="{ sortDirections: ['ascend', 'descend'] }">
        <template #cell="{ record }">{{ formatTime(record.updated_at) }}</template>
      </a-table-column>
      <a-table-column title="操作" :width="90" align="center" fixed="right">
        <template #cell="{ record }">
          <div class="row-actions">
            <a-tooltip :content="record.edit.disabled ? record.edit.reason : '编辑定义'">
              <span
                ><a-button
                  size="mini"
                  type="text"
                  :disabled="record.edit.disabled"
                  aria-label="编辑定义"
                  @click.stop="$emit('edit', record.factor_id)"
                  ><icon-edit /></a-button
              ></span>
            </a-tooltip>
            <a-tooltip :content="record.remove.disabled ? record.remove.reason : '删除定义'">
              <span @click.stop>
                <a-popconfirm content="删除后不可恢复，确认继续？" @ok="$emit('remove', record.factor_id)">
                  <a-button size="mini" type="text" status="danger" :disabled="record.remove.disabled" aria-label="删除定义"
                    ><icon-delete
                  /></a-button>
                </a-popconfirm>
              </span>
            </a-tooltip>
          </div>
        </template>
      </a-table-column>
    </template>
    <template #empty>
      <a-empty :description="emptyText" />
    </template>
  </a-table>
</template>

<script setup lang="ts">
import { formatTime } from "@/views/data/shared/metadata-utils";
import { factorTypeLabel } from "@/views/factor/shared/status";
import type { DefinitionRow } from "./definitions-model";

const props = defineProps<{
  rows: DefinitionRow[];
  loading: boolean;
  pagination: Record<string, unknown>;
  selectedKeys: string[];
  emptyText: string;
}>();

const emit = defineEmits<{
  select: [keys: string[]];
  detail: [factorId: string];
  edit: [factorId: string];
  remove: [factorId: string];
  openTask: [setId: string];
  pageChange: [page: number];
  pageSizeChange: [pageSize: number];
}>();

function selectAll(checked: boolean) {
  emit("select", checked ? props.rows.map(item => item.factor_id) : []);
}

/** 与字段页一致：点行打开详情，点按钮、链接、勾选框不触发。 */
function onRowClick(record: DefinitionRow, event?: MouseEvent) {
  const target = event?.target as HTMLElement | null;
  if (target?.closest("button, a, input, .arco-checkbox, .arco-table-checkbox")) return;
  emit("detail", record.factor_id);
}
</script>

<style scoped>
.factor-name {
  display: flex;
  min-width: 0;
  align-items: baseline;
  gap: var(--moox-space-1);
  line-height: 1.35;
  white-space: nowrap;
}
.factor-name strong,
.factor-name span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.factor-name strong {
  min-width: 0;
  color: var(--color-text-1);
}
.factor-name span {
  min-width: 0;
  color: var(--color-text-3);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.usage-empty {
  color: var(--color-text-3);
}
.usage-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.usage-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 1px 8px;
  color: var(--color-text-2);
  font-size: 12px;
  background: var(--color-fill-2);
  border: 1px solid var(--color-border-2);
  border-radius: 10px;
  cursor: pointer;
}
.usage-chip:hover {
  border-color: rgb(var(--primary-6));
}
.usage-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-neutral-5);
}
.usage-dot--enabled {
  background: rgb(var(--success-6));
}
.row-actions {
  display: inline-flex;
  gap: 2px;
}
:deep(.arco-table-tr) {
  cursor: pointer;
}
</style>
