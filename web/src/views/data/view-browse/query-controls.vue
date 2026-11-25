<template>
  <section class="view-query-panel">
    <div class="filter-grid">
      <div v-if="mode === 'record'" class="filter-item record-keyword-filter">
        <label>全文检索:</label>
        <div class="filter-control">
          <a-input v-model="recordKeyword" allow-clear placeholder="关键词" @press-enter="emit('recordSearch')" />
          <span class="operator-static">⌕</span>
        </div>
      </div>

      <div
        v-for="filter in filters"
        :key="filter.fieldName"
        class="filter-item"
        :class="{ 'range-filter-item': filter.operator === 'range' }"
      >
        <label :title="filterFieldLabel(filter.fieldName)">{{ filterFieldLabel(filter.fieldName) }}:</label>
        <div
          class="filter-control"
          :class="{ 'empty-filter-control': filter.operator === 'empty' || filter.operator === 'not_empty' }"
        >
          <template v-if="filter.operator === 'range'">
            <a-input
              v-model="filter.startValue"
              allow-clear
              :placeholder="filterPlaceholder(filter.fieldName, '开始值')"
              @press-enter="emit('query')"
            />
            <span class="range-separator">-</span>
            <a-input
              v-model="filter.endValue"
              allow-clear
              :placeholder="filterPlaceholder(filter.fieldName, '结束值')"
              @press-enter="emit('query')"
            />
          </template>
          <a-input
            v-else-if="filter.operator !== 'empty' && filter.operator !== 'not_empty'"
            v-model="filter.value"
            allow-clear
            :placeholder="filterPlaceholder(filter.fieldName, '检索值')"
            @press-enter="emit('query')"
          />
          <span v-else class="empty-filter-placeholder">{{ filter.operator === "empty" ? "为空" : "非空" }}</span>
          <a-dropdown trigger="click" @select="setFilterOperator(filter, $event)">
            <button class="operator-button" type="button" :title="filterOperatorTitle(filter.operator)">
              {{ filterOperatorSymbol(filter.operator) }}
            </button>
            <template #content>
              <a-doption v-for="option in filterOperatorOptions" :key="option.value" :value="option.value">
                {{ option.label }}
              </a-doption>
            </template>
          </a-dropdown>
        </div>
      </div>
    </div>

    <div class="query-actions">
      <a-button size="small" type="primary" :loading="loading" @click="emit('query')">查询</a-button>
      <a-button
        v-if="mode === 'time_series' && klineAvailable"
        size="small"
        type="outline"
        :loading="klineLoading"
        @click="emit('kline')"
      >
        <template #icon><icon-bar-chart /></template>
        K线
      </a-button>
      <a-button size="small" @click="emit('reset')">清空</a-button>
      <slot name="toolbar-extra" />
    </div>

    <div v-if="$slots['query-extra']" class="query-extra">
      <slot name="query-extra" />
    </div>
    <p v-if="previewRangeText" class="preview-range-hint">{{ previewRangeText }}</p>
  </section>
</template>

<script setup lang="ts">
import { zoneSuffix } from "@/utils/time-zone";
import type { ViewBrowseMode, ViewFilterFieldOption, ViewFilterOperator, ViewFilterState } from "./view-browse-utils";

defineOptions({ name: "ViewQueryControls" });

const props = defineProps<{
  mode: ViewBrowseMode;
  fieldOptions: ViewFilterFieldOption[];
  loading?: boolean;
  klineAvailable?: boolean;
  klineLoading?: boolean;
  previewRangeText?: string;
  zone: string;
  /** 没有检索项时的列标题（检索项都来自数据集列，通常用不到）。 */
  columnTitle: (name: string) => string;
}>();

const filters = defineModel<ViewFilterState[]>("filters", { required: true });
const recordKeyword = defineModel<string>("recordKeyword", { default: "" });

const emit = defineEmits<{
  (event: "query"): void;
  (event: "recordSearch"): void;
  (event: "reset"): void;
  (event: "kline"): void;
}>();

const filterOperatorOptions: Array<{ label: string; value: ViewFilterOperator }> = [
  { label: "% 类似", value: "like" },
  { label: "ABC 开头等于", value: "prefix" },
  { label: "ABC 结尾等于", value: "suffix" },
  { label: "= 等于", value: "eq" },
  { label: "≠ 不等于", value: "neq" },
  { label: "⊂ 包含", value: "contains" },
  { label: "⊄ 不包含", value: "not_contains" },
  { label: "↔ 范围", value: "range" },
  { label: "○ 为空", value: "empty" },
  { label: "Ø 非空", value: "not_empty" }
];
const filterOperatorSymbols: Partial<Record<ViewFilterOperator, string>> = {
  like: "%",
  prefix: "Ab",
  suffix: "bA",
  eq: "=",
  neq: "≠",
  contains: "⊂",
  not_contains: "⊄",
  range: "↔",
  empty: "○",
  not_empty: "Ø"
};

function setFilterOperator(filter: ViewFilterState, value: string | number | Record<string, unknown> | undefined) {
  const next = typeof value === "string" ? value : "";
  if (!filterOperatorOptions.some(option => option.value === next)) return;
  filter.operator = next as ViewFilterOperator;
  if (next === "empty" || next === "not_empty") {
    filter.value = "";
    filter.startValue = "";
    filter.endValue = "";
  } else if (next === "range") {
    filter.value = "";
  } else {
    filter.startValue = "";
    filter.endValue = "";
  }
}

function filterOperatorSymbol(operator: ViewFilterOperator) {
  return filterOperatorSymbols[operator] || "%";
}

function filterOperatorTitle(operator: ViewFilterOperator) {
  return filterOperatorOptions.find(option => option.value === operator)?.label || "检索类型";
}

function filterFieldLabel(fieldName: string) {
  return props.fieldOptions.find(item => item.value === fieldName)?.label || props.columnTitle(fieldName);
}

/** TIME 字段的输入框写清格式和时区，其余用原来的占位。 */
function filterPlaceholder(fieldName: string, fallback: string) {
  const type = props.fieldOptions.find(item => item.value === fieldName)?.valueType;
  return type === "FIELD_VALUE_TYPE_TIME" || type === 5
    ? `YYYY-MM-DD HH:mm:ss（${zoneSuffix(Date.now(), props.zone)}）`
    : fallback;
}
</script>

<style scoped>
.view-query-panel {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 14px 18px;
  align-items: start;
  width: 100%;
  max-width: 100%;
  min-width: 0;
  box-sizing: border-box;
  margin-bottom: var(--moox-space-3);
  padding: 18px var(--moox-space-5);
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}

.filter-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(220px, 100%), 1fr));
  gap: 14px 28px;
  width: 100%;
  min-width: 0;
}

.filter-item {
  display: grid;
  grid-template-columns: minmax(72px, max-content) minmax(0, 1fr);
  gap: var(--moox-space-2);
  align-items: center;
  min-width: 0;
}

.filter-item label {
  overflow: hidden;
  max-width: 84px;
  color: var(--color-text-2);
  font-weight: 500;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.filter-control {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 34px;
  gap: var(--moox-space-2);
  align-items: center;
  min-width: 0;
}

.range-filter-item {
  grid-column: span 2;
}

.range-filter-item .filter-control {
  grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr) 34px;
}

.empty-filter-control {
  grid-template-columns: minmax(0, 1fr) 34px;
}

.range-separator {
  color: var(--color-text-3);
}

.empty-filter-placeholder {
  display: flex;
  align-items: center;
  height: 32px;
  padding: 0 var(--moox-space-3);
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
  color: var(--color-text-3);
  background: var(--color-fill-1);
}

.operator-button,
.operator-static {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 32px;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
  color: var(--color-text-2);
  font-weight: 600;
  background: var(--color-bg-1);
}

.operator-button {
  cursor: pointer;
}

.operator-button:hover {
  border-color: rgb(var(--primary-6));
  color: rgb(var(--primary-6));
}

/* 调用方追加的检索行（例如因子条件）横跨整行，放在栅格与操作按钮下方。 */
.preview-range-hint {
  margin: 8px 0 0;
  color: var(--color-text-3);
  font-size: 12px;
}

.query-extra {
  grid-column: 1 / -1;
  min-width: 0;
}

.query-actions {
  display: flex;
  gap: var(--moox-space-2);
  align-items: center;
  justify-content: flex-end;
  min-width: 126px;
}

@media (max-width: 560px) {
  .view-query-panel {
    grid-template-columns: 1fr;
  }

  .filter-grid {
    grid-template-columns: 1fr;
  }

  .range-filter-item {
    grid-column: span 1;
  }

  .query-actions {
    justify-content: flex-start;
  }

  .filter-item {
    grid-template-columns: 92px minmax(0, 1fr);
  }
}
</style>
