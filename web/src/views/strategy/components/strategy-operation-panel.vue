<template>
  <div class="operation-bar">
    <span>Runner 控制</span>
    <a-space>
      <a-popconfirm v-if="status === 'ENABLED'" content="停用后不再计算新目标，确认继续？" @ok="change('DISABLED')">
        <a-button status="warning" :loading="loading">停用</a-button>
      </a-popconfirm>
      <a-popconfirm v-else content="启用时会校验 Logical Account ownership，确认继续？" @ok="change('ENABLED')">
        <a-button type="primary" status="success" :loading="loading">启用</a-button>
      </a-popconfirm>
    </a-space>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { setRunnerStatus } from "@/api/strategy";

const props = defineProps<{ runnerId: string; status: string }>();
const emit = defineEmits<{ changed: [] }>();
const loading = ref(false);

async function change(status: "ENABLED" | "DISABLED") {
  loading.value = true;
  try {
    await setRunnerStatus(props.runnerId, status);
    Message.success(status === "ENABLED" ? "Runner 已启用" : "Runner 已停用");
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
