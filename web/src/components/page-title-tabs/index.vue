<template>
  <div class="page-title-tabs" role="tablist" :aria-label="props.ariaLabel">
    <button
      v-for="item in props.items"
      :key="item.key"
      class="page-title-tab"
      :class="{ active: item.key === props.modelValue }"
      type="button"
      role="tab"
      :aria-selected="item.key === props.modelValue"
      @click="selectTab(item.key)"
    >
      {{ item.label }}
    </button>
  </div>
</template>

<script setup lang="ts">
interface PageTitleTab {
  key: string;
  label: string;
}

const props = withDefaults(
  defineProps<{
    modelValue: string;
    items: readonly PageTitleTab[];
    ariaLabel?: string;
  }>(),
  {
    ariaLabel: "页面视图"
  }
);

const emit = defineEmits<{
  "update:modelValue": [value: string];
  change: [value: string];
}>();

function selectTab(key: string) {
  if (key === props.modelValue) return;
  emit("update:modelValue", key);
  emit("change", key);
}
</script>

<style scoped>
.page-title-tabs {
  display: inline-flex;
  align-items: center;
  gap: var(--moox-space-6);
  min-height: 32px;
}

.page-title-tab {
  position: relative;
  padding: 0 0 var(--moox-space-2);
  color: var(--color-text-3);
  font-size: 20px;
  font-weight: 600;
  line-height: 28px;
  white-space: nowrap;
  cursor: pointer;
  background: transparent;
  border: 0;
  transition: color 0.16s ease;
}

.page-title-tab:hover {
  color: rgb(var(--primary-6));
}

.page-title-tab.active {
  color: var(--color-text-1);
}

.page-title-tab.active::after {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  height: 3px;
  background: rgb(var(--primary-6));
  border-radius: 3px;
  content: "";
}
</style>
