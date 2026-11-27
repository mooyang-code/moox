<template>
  <div class="group-filter-panel">
    <div class="group-filter-head">
      <strong>{{ title }}</strong>
    </div>
    <a-spin :loading="loading" class="group-filter-spin">
      <button
        class="group-item"
        :class="{ active: selected === '' }"
        :aria-current="selected === '' ? 'true' : undefined"
        type="button"
        @click="emit('select', '')"
      >
        <icon-apps /><span class="group-label">{{ allLabel }}</span
        ><span class="group-count">{{ total }}</span>
      </button>

      <button
        v-for="item in items"
        :key="item.key"
        class="group-item"
        :class="{ active: selected === item.key }"
        :aria-current="selected === item.key ? 'true' : undefined"
        type="button"
        @click="emit('select', item.key)"
      >
        <icon-folder /><span class="group-label" :title="item.label">{{ item.label }}</span
        ><span class="group-count">{{ item.count }}</span>
      </button>

      <a-empty v-if="!loading && !items.length" :description="emptyText" />
    </a-spin>
  </div>
</template>

<script setup lang="ts">
export interface GroupFilterItem {
  key: string;
  label: string;
  count: number;
}

defineOptions({ name: "GroupFilterPanel" });

withDefaults(
  defineProps<{
    title: string;
    allLabel: string;
    items: ReadonlyArray<GroupFilterItem>;
    total: number;
    selected: string;
    loading?: boolean;
    emptyText?: string;
  }>(),
  {
    loading: false,
    emptyText: "暂无分组"
  }
);

const emit = defineEmits<{
  select: [key: string];
}>();
</script>

<style scoped>
.group-filter-panel {
  height: 100%;
  min-height: 0;
  padding: 10px var(--moox-space-2);
}

.group-filter-head {
  display: flex;
  height: 34px;
  padding: 0 6px 6px 10px;
  align-items: center;
  justify-content: space-between;
}

.group-filter-spin {
  display: block;
  min-height: 160px;
}

.group-item {
  display: flex;
  width: 100%;
  min-width: 0;
  height: 34px;
  padding: 0 var(--moox-space-2);
  margin-top: 2px;
  border: 0;
  border-radius: 4px;
  align-items: center;
  gap: var(--moox-space-2);
  background: transparent;
  color: var(--color-text-2);
  cursor: pointer;
  text-align: left;
}

.group-item:first-child {
  margin-top: 0;
}

.group-item:hover {
  background: var(--color-fill-2);
}

.group-item.active {
  background: rgb(var(--primary-1));
  color: rgb(var(--primary-6));
  font-weight: 600;
}

.group-item :deep(svg) {
  flex: 0 0 auto;
}

.group-label {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.group-count {
  flex: 0 0 auto;
  margin-left: auto;
  color: var(--color-text-3);
  font-size: 12px;
  font-weight: 400;
}
</style>
