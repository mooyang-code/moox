<template>
  <div class="operation-bar">
    <span>实例控制</span>
    <a-space>
      <a-popconfirm v-if="currentEnabled" content="停用后不再计算新目标，确认继续？" @ok="change(false)">
        <a-button status="warning" :loading="loading">停用</a-button>
      </a-popconfirm>
      <a-popconfirm v-else content="启用时会校验组合账户归属，确认继续？" @ok="change(true)">
        <a-button type="primary" status="success" :loading="loading">启用</a-button>
      </a-popconfirm>
    </a-space>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { setInstanceEnabled } from "@/api/strategy";

const props = defineProps<{ instanceId: string; enabled?: boolean }>();
const emit = defineEmits<{ changed: [] }>();
const loading = ref(false);
const currentEnabled = computed(() => Boolean(props.enabled));

async function change(enabled: boolean) {
  loading.value = true;
  try {
    await setInstanceEnabled(props.instanceId, enabled);
    Message.success(enabled ? "实例已启用" : "实例已停用");
    emit("changed");
  } finally {
    loading.value = false;
  }
}
</script>

<style scoped>
.operation-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
  padding: 10px 12px;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}
.operation-bar > span {
  color: var(--color-text-2);
  font-weight: 600;
}
</style>
