<template>
  <div class="operation-bar">
    <div><strong>实例控制</strong><span class="hint">启停只控制策略计算与目标投递，不执行清仓。</span></div>
    <a-button
      :type="currentEnabled ? 'outline' : 'primary'"
      :status="currentEnabled ? 'warning' : 'success'"
      :loading="loading"
      :disabled="uncertain"
      @click="change(!currentEnabled)"
    >
      {{ currentEnabled ? "停用实例" : "启用实例" }}
    </a-button>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { confirmInstanceToggle } from "@/views/strategy/components/instance-toggle";

const props = defineProps<{ instanceId: string; enabled: boolean; uncertain?: boolean }>();
const emit = defineEmits<{ started: []; changed: []; failed: [unknown] }>();
const loading = ref(false);
const currentEnabled = computed(() => Boolean(props.enabled));
// 用显式确认对话框而非 popconfirm：popconfirm 点外部会静默关闭，用户误以为已提交。
function change(enabled: boolean) {
  if (loading.value || props.uncertain) return;
  confirmInstanceToggle(props.instanceId, enabled, {
    onStart: () => {
      loading.value = true;
      emit("started");
    },
    onFailed: error => emit("failed", error),
    onSettled: () => {
      loading.value = false;
      emit("changed");
    }
  });
}
</script>

<style scoped>
.operation-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-2);
  padding: 12px 0;
  border-top: 1px solid var(--color-border-2);
  border-bottom: 1px solid var(--color-border-2);
}
.operation-bar > div {
  display: grid;
  gap: 3px;
}
.hint {
  color: var(--color-text-3);
  font-size: 12px;
}
@media (max-width: 640px) {
  .operation-bar {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
