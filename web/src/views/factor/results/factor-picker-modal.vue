<template>
  <a-modal
    :visible="props.visible"
    :title="props.mode === 'condition' ? (props.initial ? '修改因子条件' : '添加因子条件') : '添加外显因子列'"
    width="900px"
    :ok-button-props="{ disabled: !canConfirm }"
    unmount-on-close
    @ok="confirm"
    @cancel="emit('update:visible', false)"
  >
    <div class="picker">
      <ul class="picker-categories" aria-label="因子分类">
        <li :class="{ active: !category }" @click="selectCategory('')">
          <span>全部</span><span class="count">{{ keywordMatched.length }}</span>
        </li>
        <li v-for="id in props.scope.categoryOrder" :key="id" :class="{ active: category === id }" @click="selectCategory(id)">
          <span>{{ props.scope.categoryNames.get(id) || id }}</span
          ><span class="count">{{ counts.get(id) || 0 }}</span>
        </li>
      </ul>
      <div class="picker-main">
        <a-input-search v-model="keyword" allow-clear placeholder="搜索输出列、因子或成员" @input="page = 1" @clear="page = 1" />
        <a-table
          row-key="column"
          size="small"
          :data="filtered"
          :bordered="{ cell: true }"
          :row-selection="{ type: 'radio' }"
          :selected-keys="selected ? [selected] : []"
          :pagination="{ current: page, pageSize: FACTOR_PAGE_SIZE, showTotal: true, hideOnSinglePage: true }"
          :scroll="{ y: 360 }"
          @page-change="page = $event"
          @row-click="(record: FactorOutputItem) => (selected = record.column)"
          @select="(keys: (string | number)[]) => (selected = String(keys[0] ?? ''))"
        >
          <template #columns>
            <a-table-column title="输出列" data-index="column" :ellipsis="true" :tooltip="true" />
            <a-table-column title="因子">
              <template #cell="{ record }"
                >{{ record.memberKey
                }}<span v-if="record.memberKey !== record.factorId" class="muted">（{{ record.factorId }}）</span></template
              >
            </a-table-column>
            <a-table-column title="分类" :width="120">
              <template #cell="{ record }">{{ props.scope.categoryNames.get(record.categoryId) || record.categoryId }}</template>
            </a-table-column>
          </template>
        </a-table>
      </div>
    </div>

    <div v-if="props.mode === 'condition'" class="picker-condition">
      <span class="picker-target">{{ selected || "请先在上面选择一个输出列" }}</span>
      <a-select v-model="op" :options="CONDITION_OPS" style="width: 100px" />
      <template v-if="op === 'range'">
        <a-input v-model="minText" class="picker-value" placeholder="下限" allow-clear @press-enter="confirm" />
        <span>～</span>
        <a-input v-model="maxText" class="picker-value" placeholder="上限" allow-clear @press-enter="confirm" />
      </template>
      <a-input v-else v-model="valueText" class="picker-value" placeholder="数值" allow-clear @press-enter="confirm" />
      <span v-if="valueError" class="picker-error">{{ valueError }}</span>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { CONDITION_OPS, isValidCondition, parseNumber, type FactorCondition, type FactorConditionOp } from "./factor-condition";
import { FACTOR_PAGE_SIZE, countByCategory, filterOutputs, type FactorOutputItem, type FactorScope } from "./factor-scope";

const props = withDefaults(
  defineProps<{
    visible: boolean;
    scope: FactorScope;
    /** condition：选输出列并填算子与数值；pin：只选输出列。 */
    mode: "condition" | "pin";
    /** 修改已有条件时的初始值。 */
    initial?: FactorCondition;
    /** 不出现在列表里的列（例如已钉住的列）。 */
    excluded?: string[];
  }>(),
  { initial: undefined, excluded: () => [] }
);

const emit = defineEmits<{
  (event: "update:visible", value: boolean): void;
  (event: "pick", value: { column: string; condition?: FactorCondition }): void;
}>();

const category = ref("");
const keyword = ref("");
const page = ref(1);
const selected = ref("");
const op = ref<FactorConditionOp>("gt");
const valueText = ref("");
const minText = ref("");
const maxText = ref("");

const candidates = computed(() => {
  const excluded = new Set(props.excluded);
  return props.scope.outputs.filter(item => !excluded.has(item.column));
});
const keywordMatched = computed(() => filterOutputs(candidates.value, { keyword: keyword.value }));
const counts = computed(() => countByCategory(keywordMatched.value));
const filtered = computed(() => filterOutputs(keywordMatched.value, { category: category.value }));

const draft = computed<FactorCondition>(() =>
  op.value === "range"
    ? { column: selected.value, op: op.value, min: parseNumber(minText.value), max: parseNumber(maxText.value) }
    : { column: selected.value, op: op.value, value: parseNumber(valueText.value) }
);
const valueError = computed(() => {
  if (op.value === "range") {
    if (!minText.value.trim() || !maxText.value.trim()) return "";
    if (draft.value.min === undefined || draft.value.max === undefined) return "请输入有效数字";
    return draft.value.min > draft.value.max ? "下限不能大于上限" : "";
  }
  if (!valueText.value.trim()) return "";
  return draft.value.value === undefined ? "请输入有效数字" : "";
});
const canConfirm = computed(() =>
  props.mode === "pin" ? Boolean(selected.value) : Boolean(selected.value) && isValidCondition(draft.value)
);

function selectCategory(id: string) {
  category.value = id;
  page.value = 1;
}

function confirm() {
  if (!canConfirm.value) return;
  emit("pick", props.mode === "pin" ? { column: selected.value } : { column: selected.value, condition: { ...draft.value } });
  emit("update:visible", false);
}

watch(
  () => props.visible,
  visible => {
    if (!visible) return;
    const initial = props.initial;
    category.value = "";
    keyword.value = "";
    page.value = 1;
    selected.value = initial?.column ?? "";
    op.value = initial?.op ?? "gt";
    valueText.value = initial?.value === undefined ? "" : String(initial.value);
    minText.value = initial?.min === undefined ? "" : String(initial.min);
    maxText.value = initial?.max === undefined ? "" : String(initial.max);
  },
  { immediate: true }
);
</script>

<style scoped lang="scss">
.picker {
  display: grid;
  grid-template-columns: 180px minmax(0, 1fr);
  gap: var(--moox-space-3);
}

.picker-categories {
  max-height: 440px;
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

.picker-main {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-2);
  min-width: 0;
}

.muted {
  color: var(--color-text-3);
}

.picker-condition {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-3);
  padding-top: var(--moox-space-3);
  border-top: 1px solid var(--color-border-2);
}

.picker-target {
  min-width: 180px;
  font-family: var(--moox-font-mono, monospace);
}

.picker-value {
  width: 140px;
}

.picker-error {
  color: rgb(var(--danger-6));
}

@media (max-width: 700px) {
  .picker {
    grid-template-columns: 1fr;
  }

  .picker-categories {
    display: flex;
    flex-wrap: wrap;
    max-height: none;
  }
}
</style>
