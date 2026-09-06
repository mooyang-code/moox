<template>
  <div class="operation-bar">
    <div><strong>实例控制</strong><span class="hint">启停只控制策略计算与目标投递，不执行清仓。</span></div>
    <a-popconfirm
      :content="currentEnabled ? '停用后不会再计算新目标，也不会自动清仓。确认继续？' : '启用会校验绑定并在交易模式下认领账户会话。确认继续？'"
      @ok="change(!currentEnabled)"
    >
      <a-button :type="currentEnabled ? 'outline' : 'primary'" :status="currentEnabled ? 'warning' : 'success'" :loading="loading" :disabled="uncertain">
        {{ currentEnabled ? "停用实例" : "启用实例" }}
      </a-button>
    </a-popconfirm>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { setInstanceEnabled } from "@/api/strategy";

const props = defineProps<{ instanceId: string; enabled: boolean; uncertain?: boolean }>();
const emit = defineEmits<{ started: []; changed: []; failed: [unknown] }>();
const loading = ref(false);
const currentEnabled = computed(() => Boolean(props.enabled));
async function change(enabled: boolean) {
  if (loading.value || props.uncertain) return;
  loading.value = true;
  emit("started");
  try {
    await setInstanceEnabled(props.instanceId, enabled);
    Message.success(enabled ? "实例已启用" : "实例已停用");
  } catch (error) {
    Message.error("控制请求失败，正在重新读取实例状态");
    emit("failed", error);
  } finally {
    loading.value = false;
    emit("changed");
  }
}
</script>

<style scoped>
.operation-bar { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: var(--moox-space-2); padding: 12px 0; border-top: 1px solid var(--color-border-2); border-bottom: 1px solid var(--color-border-2); }
.operation-bar > div { display: grid; gap: 3px; }
.hint { color: var(--color-text-3); font-size: 12px; }
@media (max-width: 640px) { .operation-bar { align-items: flex-start; flex-direction: column; } }
</style>
