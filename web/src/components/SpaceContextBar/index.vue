<template>
  <div class="space-context-bar">
    <span>当前空间</span>
    <strong>{{ spaceStore.selectedSpace?.name || '未选择' }}</strong>
    <code v-if="spaceStore.selectedSpaceId">{{ spaceStore.selectedSpaceId }}</code>
  </div>
</template>

<script setup lang="ts">
import { useSpaceStore } from '@/store/modules/space';

defineOptions({ name: 'SpaceContextBar' });

const spaceStore = useSpaceStore();
</script>

<style scoped>
.space-context-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  padding: 8px 12px;
  margin-bottom: 12px;
  color: var(--color-text-2);
  background: var(--color-fill-2);
  border-radius: 6px;
}

.space-context-bar strong {
  color: var(--color-text-1);
}

.space-context-bar code {
  padding: 1px 6px;
  font-size: 12px;
  color: var(--color-text-3);
  background: var(--color-fill-3);
  border-radius: 4px;
}
</style>
