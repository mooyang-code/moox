<template>
  <a-select
    :model-value="props.modelValue"
    class="task-select"
    :placeholder="props.placeholder"
    :loading="store.loading"
    :disabled="!options.length"
    @change="onChange"
  >
    <a-option v-for="item in options" :key="item.value" :value="item.value">
      <span class="task-select__dot" :class="`task-select__dot--${item.status}`"></span>
      {{ item.label }}
    </a-option>
  </a-select>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { FactorTaskInfo } from "@/api/factor/types";
import { useFactorStore } from "@/store/modules/factor";

defineOptions({ name: "FactorTaskSelect" });

const props = withDefaults(defineProps<{ modelValue: string; placeholder?: string }>(), { placeholder: "选择计算任务" });
const emit = defineEmits<{ "update:modelValue": [taskId: string]; change: [taskId: string] }>();

const store = useFactorStore();

const options = computed(() =>
  store.sets.map((item: FactorTaskInfo) => ({
    value: item.factor_task.task_id,
    label: store.setLabel(item.factor_task),
    status: item.factor_task.status
  }))
);

function onChange(value: unknown) {
  const taskId = String(value ?? "");
  emit("update:modelValue", taskId);
  emit("change", taskId);
}
</script>

<style scoped>
.task-select {
  min-width: 220px;
}

.task-select__dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  margin-right: 6px;
  border-radius: 50%;
  background: var(--color-neutral-5);
}

.task-select__dot--enabled {
  background: rgb(var(--success-6));
}

.task-select__dot--pending {
  background: rgb(var(--primary-6));
}

.task-select__dot--disabled {
  background: rgb(var(--warning-6));
}
</style>
