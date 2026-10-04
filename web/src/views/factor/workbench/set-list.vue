<template>
  <div class="set-list">
    <div class="set-list__head">
      <span class="set-list__title">因子集</span>
      <a-button type="primary" status="success" size="mini" @click="emit('create')">
        <template #icon><icon-plus /></template>
        新建
      </a-button>
    </div>
    <a-spin :loading="store.loading" class="set-list__spin">
      <a-empty v-if="!store.sets.length && !store.loading" description="当前空间没有因子集" />
      <button
        v-for="item in store.sets"
        :key="item.factor_set.set_id"
        type="button"
        class="set-item"
        :class="{ 'set-item--active': item.factor_set.set_id === store.currentSetId }"
        @click="emit('select', item.factor_set.set_id)"
      >
        <span class="set-item__name">{{ item.factor_set.source_dataset_id }} · {{ item.factor_set.freq }}</span>
        <span class="set-item__meta">
          <a-tag size="small" :color="healthOf(item).color">{{ healthOf(item).label }}</a-tag>
          <span>{{ item.factors?.length || 0 }} 个因子</span>
          <span v-if="item.last_run?.last_period_time">滞后 {{ formatLag(item.last_run.lag_seconds) }}</span>
        </span>
      </button>
    </a-spin>
  </div>
</template>

<script setup lang="ts">
import type { FactorSetInfo } from "@/api/factor/types";
import { useFactorStore } from "@/store/modules/factor";
import { formatLag, setHealth } from "./health";

defineOptions({ name: "FactorSetList" });

const emit = defineEmits<{ (e: "create"): void; (e: "select", setId: string): void }>();
const store = useFactorStore();

const healthOf = (item: FactorSetInfo) => setHealth(item.factor_set, item.last_run);
</script>

<style scoped>
.set-list {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
  gap: var(--moox-space-3);
}

.set-list__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.set-list__title {
  font-size: 15px;
  font-weight: 600;
}

.set-list__spin {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  gap: var(--moox-space-2);
  overflow-y: auto;
}

.set-item {
  display: flex;
  width: 100%;
  flex-direction: column;
  gap: 6px;
  padding: 10px 12px;
  margin-bottom: var(--moox-space-2);
  color: var(--color-text-1);
  text-align: left;
  cursor: pointer;
  background: var(--color-bg-2);
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}

.set-item:hover {
  border-color: rgb(var(--primary-6));
}

.set-item--active {
  background: var(--color-primary-light-1);
  border-color: rgb(var(--primary-6));
}

.set-item__name {
  overflow: hidden;
  font-size: 13px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.set-item__meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  color: var(--color-text-3);
  font-size: 12px;
}
</style>
