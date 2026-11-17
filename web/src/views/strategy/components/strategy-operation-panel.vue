<template>
  <div class="operation-bar">
    <div><strong>实例控制</strong><span class="hint">启停只控制策略计算与目标投递，不执行清仓。</span></div>
    <span class="operation-actions">
      <a-button
        :type="currentEnabled ? 'outline' : 'primary'"
        :status="currentEnabled ? 'warning' : 'success'"
        :loading="loading"
        :disabled="uncertain"
        @click="change(!currentEnabled)"
      >
        {{ currentEnabled ? "停用实例" : "启用实例" }}
      </a-button>
      <!-- 停用时 Trade 没确认关闭会话的重试入口：Strategy 不做后台重试，再停用一次才会重新通知 Trade -->
      <a-button v-if="!currentEnabled" type="text" status="warning" :disabled="loading || uncertain" @click="redisable">
        再次停用
      </a-button>
    </span>
    <InstanceEnableDialog ref="enableDialog" />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { confirmInstanceRedisable, confirmInstanceToggle } from "@/views/strategy/components/instance-toggle";
import InstanceEnableDialog from "@/views/strategy/components/instance-enable-dialog.vue";

const props = defineProps<{
  instanceId: string;
  enabled: boolean;
  uncertain?: boolean;
  /** 绑定的组合账户；有值时启用要用写明账户和环境的确认框 */
  logicalAccountId?: string;
  factorTasks?: Record<string, string>;
}>();
const emit = defineEmits<{ started: []; changed: []; failed: [unknown] }>();
const loading = ref(false);
const enableDialog = ref<InstanceType<typeof InstanceEnableDialog> | null>(null);
const currentEnabled = computed(() => Boolean(props.enabled));
const hooks = {
  onStart: () => {
    loading.value = true;
    emit("started");
  },
  onFailed: (error: unknown) => emit("failed", error),
  onSettled: () => {
    loading.value = false;
    emit("changed");
  }
};
// 用显式确认对话框而非 popconfirm：popconfirm 点外部会静默关闭，用户误以为已提交。
function change(enabled: boolean) {
  if (loading.value || props.uncertain) return;
  if (enabled && props.logicalAccountId) {
    enableDialog.value?.open(
      { instance_id: props.instanceId, logical_account_id: props.logicalAccountId, factor_tasks: props.factorTasks },
      hooks
    );
    return;
  }
  confirmInstanceToggle(props.instanceId, enabled, hooks);
}
function redisable() {
  if (loading.value || props.uncertain) return;
  confirmInstanceRedisable(props.instanceId, hooks);
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
.operation-actions {
  display: flex;
  align-items: center;
  gap: 4px;
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
