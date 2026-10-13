<template>
  <div class="section-title list-title">回放记录</div>
  <a-list :data="replays" :loading="loading" :bordered="false" size="small">
    <template #item="{ item }">
      <a-list-item :class="{ active: item.replay_id === selectedId }">
        <button
          type="button"
          class="replay-row"
          :aria-current="item.replay_id === selectedId ? 'true' : undefined"
          @click="emit('select', item.replay_id)"
        >
          <span class="replay-line">
            <a-tag size="small" :color="replayStatusColor(item.status)">{{ replayStatusLabel(item.status) }}</a-tag>
            <span>{{ replaySourceLabel(item, strategies) }}</span>
          </span>
          <span class="replay-line muted mono">{{ item.view_id }}</span>
          <span class="replay-line muted">{{ replayRangeLabel(item) }}</span>
          <span v-if="item.status === 'running' && item.progress_time" class="replay-line muted">
            进度 {{ formatUtcTime(item.progress_time) }}
          </span>
        </button>
      </a-list-item>
    </template>
  </a-list>
  <a-pagination
    v-if="total > pageSize"
    class="replay-pager"
    size="small"
    simple
    :current="page"
    :page-size="pageSize"
    :total="total"
    @change="emit('pageChange', $event)"
  />
</template>

<script setup lang="ts">
import type { Replay, Strategy } from "@/api/strategy-types";
import { formatUtcTime, replayRangeLabel, replaySourceLabel, replayStatusColor, replayStatusLabel } from "@/views/strategy/replay/model";

defineProps<{
  replays: Replay[];
  strategies: Strategy[];
  selectedId: string;
  loading: boolean;
  page: number;
  pageSize: number;
  total: number;
}>();
const emit = defineEmits<{ select: [string]; pageChange: [number] }>();
</script>

<style scoped>
.section-title {
  margin-bottom: 8px;
  font-weight: 600;
}
.list-title {
  margin-top: 20px;
}
.replay-row {
  display: grid;
  gap: 2px;
  width: 100%;
  padding: 0;
  border: none;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.replay-row:focus-visible {
  outline: 2px solid rgb(var(--arcoblue-6));
  outline-offset: 2px;
}
.replay-line {
  display: block;
}
.replay-pager {
  margin-top: 8px;
}
.active {
  background: var(--color-fill-2);
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
</style>
