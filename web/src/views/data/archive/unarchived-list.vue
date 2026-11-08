<template>
  <section v-if="error || items.length" class="unarchived" aria-label="未归档的数据集">
    <div v-if="error" class="line is-warn">
      无法读取数据集列表，未归档提醒暂不可用 · <a-link @click="emit('retry')">重试</a-link>
    </div>
    <template v-else>
      <h3>未归档的数据集</h3>
      <div v-for="item in items" :key="item.dataset_id" class="row">
        <div class="name">
          <div>{{ item.name || item.dataset_id }}</div>
          <div class="muted mono">{{ item.dataset_id }}</div>
        </div>
        <span class="freq">{{ item.freq || "—" }}</span>
        <span class="note">线上保留 {{ formatRetention(item.retention) }}，超过就按天删除，删了无法找回</span>
        <a-button type="primary" status="success" size="small" @click="emit('create', item.dataset_id)">建归档任务</a-button>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import type { Dataset } from "@/api/storage/types";
import { formatRetention } from "@/utils/retention";

/** 应该归档、还没有任务的数据集；只在有时出现。读取失败时写明“暂不可用”，不能当成全部已归档而不显示。 */
defineProps<{ items: Dataset[]; error: string }>();
const emit = defineEmits<{ create: [datasetId: string]; retry: [] }>();
</script>

<style scoped>
.unarchived {
  margin-bottom: var(--moox-space-3);
}

h3 {
  margin: 0 0 var(--moox-space-2);
  font-size: 14px;
  font-weight: 600;
}

.row {
  display: grid;
  grid-template-columns: minmax(180px, 1.2fr) 60px minmax(200px, 2fr) auto;
  gap: 12px;
  align-items: center;
  padding: 6px 0;
  border-bottom: 1px solid var(--color-border-1);
}

@media (max-width: 640px) {
  .row {
    grid-template-columns: 1fr auto;
  }
  .note {
    grid-column: 1 / -1;
  }
}

.muted {
  color: var(--color-text-3);
  font-size: 12px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.note {
  color: var(--color-text-2);
}

.is-warn {
  color: rgb(var(--orange-6));
}
</style>
