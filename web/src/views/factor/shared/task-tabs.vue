<template>
  <a-tabs :active-key="props.modelValue" type="rounded" size="medium" class="task-tabs" @change="onChange">
    <a-tab-pane v-for="item in options" :key="item.value" :title="item.label" />
  </a-tabs>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { FactorTaskInfo } from "@/api/factor/types";
import { useFactorStore } from "@/store/modules/factor";

defineOptions({ name: "FactorTaskTabs" });

/** 计算任务平铺成页签。include：只列满足条件的任务（例如结果页只列能浏览结果的任务）。 */
const props = withDefaults(defineProps<{ modelValue: string; include?: (info: FactorTaskInfo) => boolean }>(), {
  include: undefined
});
const emit = defineEmits<{ "update:modelValue": [taskId: string]; change: [taskId: string] }>();

const store = useFactorStore();

// 停用、创建中的任务在名称后注明，和策略运行结果的实例页签同一写法
const STATUS_NOTE: Record<string, string> = { disabled: "（停用）", pending: "（创建中）" };

const options = computed(() =>
  store.tasks
    .filter((item: FactorTaskInfo) => !props.include || props.include(item))
    .map((item: FactorTaskInfo) => ({
      value: item.factor_task.task_id,
      label: store.taskLabel(item.factor_task) + (STATUS_NOTE[item.factor_task.status] ?? "")
    }))
);

function onChange(value: string | number) {
  const taskId = String(value);
  emit("update:modelValue", taskId);
  emit("change", taskId);
}
</script>

<style scoped>
.task-tabs {
  min-width: 0;
}

.task-tabs :deep(.arco-tabs-content) {
  display: none;
}
</style>
