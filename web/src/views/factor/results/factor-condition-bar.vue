<template>
  <div class="factor-condition-bar">
    <span class="bar-label">因子条件:</span>
    <a-tag
      v-for="(condition, index) in props.conditions"
      :key="`${condition.column}-${index}`"
      class="condition-chip"
      closable
      color="arcoblue"
      @click="edit(index)"
      @close="remove(index)"
    >
      {{ conditionText(condition) }}
    </a-tag>
    <a-button size="mini" type="primary" status="success" :disabled="!props.scope.outputs.length" @click="add">
      <template #icon><icon-plus /></template>添加因子条件
    </a-button>
    <span v-if="props.conditions.length > 1" class="bar-hint">多个条件同时满足</span>
    <span v-else-if="!props.scope.outputs.length" class="bar-hint">当前任务没有可用的因子输出</span>

    <FactorPickerModal
      v-model:visible="pickerVisible"
      mode="condition"
      :scope="props.scope"
      :initial="editingIndex === undefined ? undefined : props.conditions[editingIndex]"
      @pick="onPick"
    />
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { conditionText, type FactorCondition } from "./factor-condition";
import type { FactorScope } from "./factor-scope";
import FactorPickerModal from "./factor-picker-modal.vue";

const props = defineProps<{ conditions: FactorCondition[]; scope: FactorScope }>();
const emit = defineEmits<{ (event: "update:conditions", value: FactorCondition[]): void }>();

const pickerVisible = ref(false);
const editingIndex = ref<number>();

function add() {
  editingIndex.value = undefined;
  pickerVisible.value = true;
}

function edit(index: number) {
  editingIndex.value = index;
  pickerVisible.value = true;
}

function remove(index: number) {
  emit(
    "update:conditions",
    props.conditions.filter((_, i) => i !== index)
  );
}

function onPick(payload: { condition?: FactorCondition }) {
  const condition = payload.condition;
  if (!condition) return;
  const next = [...props.conditions];
  if (editingIndex.value === undefined) next.push(condition);
  else next.splice(editingIndex.value, 1, condition);
  emit("update:conditions", next);
}
</script>

<style scoped lang="scss">
.factor-condition-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
  margin-top: var(--moox-space-2);
}

.bar-label {
  color: var(--color-text-2);
}

.condition-chip {
  cursor: pointer;
}

.bar-hint {
  color: var(--color-text-3);
  font-size: 12px;
}
</style>
