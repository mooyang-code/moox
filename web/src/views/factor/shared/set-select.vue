<template>
  <a-select
    :model-value="props.modelValue"
    class="set-select"
    :placeholder="props.placeholder"
    :loading="store.loading"
    :disabled="!options.length"
    @change="onChange"
  >
    <a-option v-for="item in options" :key="item.value" :value="item.value">
      <span class="set-select__dot" :class="`set-select__dot--${item.status}`"></span>
      {{ item.label }}
    </a-option>
  </a-select>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { FactorSetInfo } from "@/api/factor/types";
import { useFactorStore } from "@/store/modules/factor";

defineOptions({ name: "FactorSetSelect" });

const props = withDefaults(defineProps<{ modelValue: string; placeholder?: string }>(), { placeholder: "选择计算任务" });
const emit = defineEmits<{ "update:modelValue": [setId: string]; change: [setId: string] }>();

const store = useFactorStore();

const options = computed(() =>
  store.sets.map((item: FactorSetInfo) => ({
    value: item.factor_set.set_id,
    label: store.setLabel(item.factor_set),
    status: item.factor_set.status
  }))
);

function onChange(value: unknown) {
  const setId = String(value ?? "");
  emit("update:modelValue", setId);
  emit("change", setId);
}
</script>

<style scoped>
.set-select {
  min-width: 220px;
}

.set-select__dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  margin-right: 6px;
  border-radius: 50%;
  background: var(--color-neutral-5);
}

.set-select__dot--enabled {
  background: rgb(var(--success-6));
}

.set-select__dot--pending {
  background: rgb(var(--primary-6));
}

.set-select__dot--disabled {
  background: rgb(var(--warning-6));
}
</style>
