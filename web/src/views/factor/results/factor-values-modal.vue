<template>
  <a-modal
    :visible="props.visible"
    :title="title"
    width="960px"
    :footer="false"
    unmount-on-close
    @cancel="emit('update:visible', false)"
  >
    <div class="values">
      <ul class="values-categories" aria-label="因子分类">
        <li :class="{ active: !category }" @click="selectCategory('')">
          <span>全部</span><span class="count">{{ keywordMatched.length }}</span>
        </li>
        <li v-for="id in props.scope.categoryOrder" :key="id" :class="{ active: category === id }" @click="selectCategory(id)">
          <span>{{ props.scope.categoryNames.get(id) || id }}</span
          ><span class="count">{{ counts.get(id) || 0 }}</span>
        </li>
      </ul>
      <div class="values-main">
        <a-input-search v-model="keyword" allow-clear placeholder="搜索输出列、因子或成员" @input="page = 1" @clear="page = 1" />
        <a-alert v-if="error" type="error" show-icon>
          {{ error }}<a-button type="text" size="mini" @click="load">重试</a-button>
        </a-alert>
        <a-table
          row-key="column"
          size="small"
          :data="filtered"
          :loading="loading"
          :bordered="{ cell: true }"
          :pagination="{ current: page, pageSize: FACTOR_PAGE_SIZE, showTotal: true, hideOnSinglePage: true }"
          :scroll="{ y: 420 }"
          @page-change="page = $event"
        >
          <template #columns>
            <a-table-column title="因子" :width="200">
              <template #cell="{ record }"
                >{{ record.alias
                }}<span v-if="record.alias !== record.factorId" class="muted">（{{ record.factorId }}）</span></template
              >
            </a-table-column>
            <a-table-column title="输出列" data-index="column" :ellipsis="true" :tooltip="true" />
            <a-table-column title="参数" :width="220" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">{{ paramText(props.scope, record.alias, values) || "-" }}</template>
            </a-table-column>
            <a-table-column title="值" :width="160" align="right">
              <template #cell="{ record }">{{ loaded ? formatValue(values[record.column]) : "" }}</template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { TimeSeriesKey } from "@/api/storage/types";
import { queryTimeSeriesRows } from "@/api/storage/view";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  FACTOR_PAGE_SIZE,
  countByCategory,
  filterOutputs,
  formatValue,
  pageSlice,
  paramText,
  rowValuesRequest,
  valueColumnsFor,
  type FactorScope
} from "./factor-scope";

const props = defineProps<{
  visible: boolean;
  viewId: string;
  rowKey?: TimeSeriesKey;
  scope: FactorScope;
}>();
const emit = defineEmits<{ (event: "update:visible", value: boolean): void }>();

const category = ref("");
const keyword = ref("");
const page = ref(1);
const values = ref<Record<string, string>>({});
const loaded = ref(false);
const loading = ref(false);
const error = ref("");
const guard = createLatestRequestGuard();

const keywordMatched = computed(() => filterOutputs(props.scope.outputs, { keyword: keyword.value }));
const counts = computed(() => countByCategory(keywordMatched.value));
const filtered = computed(() => filterOutputs(keywordMatched.value, { category: category.value }));
const pageItems = computed(() => pageSlice(filtered.value, page.value));

const title = computed(() => {
  const key = props.rowKey;
  if (!key) return "因子值";
  const parts = [
    key.subject_id,
    key.freq,
    key.series_tag || "默认序列",
    key.data_time ? new Date(key.data_time).toLocaleString("zh-CN", { hour12: false }) : ""
  ];
  return `因子值 · ${parts.filter(Boolean).join(" · ")}`;
});

function selectCategory(id: string) {
  category.value = id;
  page.value = 1;
}

/** 每翻一页（或改筛选）只查这一行、这一页输出及其参数列的值。 */
async function load() {
  const key = props.rowKey;
  const columns = valueColumnsFor(props.scope, pageItems.value);
  const request = guard.begin();
  error.value = "";
  if (!key || !columns.length) {
    values.value = {};
    loaded.value = true;
    loading.value = false;
    return;
  }
  loading.value = true;
  loaded.value = false;
  try {
    const rsp = await queryTimeSeriesRows(rowValuesRequest(props.viewId, key, columns));
    if (!request.isLatest()) return;
    const next: Record<string, string> = {};
    for (const field of rsp.rows?.[0]?.fields ?? []) next[field.field_id] = fieldText(field.value);
    values.value = next;
    loaded.value = true;
  } catch (err) {
    if (!request.isLatest()) return;
    error.value = err instanceof Error ? err.message : "读取因子值失败";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function fieldText(value: unknown) {
  if (!value || typeof value !== "object") return "";
  const typed = value as Record<string, unknown>;
  const raw = typed.double_value ?? typed.int_value ?? typed.string_value ?? typed.bool_value;
  return raw === undefined || raw === null ? "" : String(raw);
}

watch(
  () => props.visible,
  visible => {
    if (visible) {
      category.value = "";
      keyword.value = "";
      page.value = 1;
      values.value = {};
      void load();
    } else {
      guard.invalidate();
    }
  },
  { immediate: true }
);

watch(
  () => pageItems.value.map(item => item.column).join(","),
  () => {
    if (props.visible) void load();
  }
);
</script>

<style scoped lang="scss">
.values {
  display: grid;
  grid-template-columns: 180px minmax(0, 1fr);
  gap: var(--moox-space-3);
}

.values-categories {
  max-height: 500px;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  list-style: none;

  li {
    display: flex;
    justify-content: space-between;
    gap: var(--moox-space-2);
    padding: 6px 10px;
    border-radius: 4px;
    cursor: pointer;

    &:hover {
      background: var(--color-fill-2);
    }

    &.active {
      color: rgb(var(--primary-6));
      background: var(--color-primary-light-1);
    }
  }

  .count {
    color: var(--color-text-3);
  }
}

.values-main {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-2);
  min-width: 0;
}

.muted {
  color: var(--color-text-3);
}

@media (max-width: 700px) {
  .values {
    grid-template-columns: 1fr;
  }

  .values-categories {
    display: flex;
    flex-wrap: wrap;
    max-height: none;
  }
}
</style>
